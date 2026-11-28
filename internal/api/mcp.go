package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// The MCP server: the Streamable HTTP transport at one path (spec §9).
//
// Hand-written rather than taken from an SDK because the surface shelf needs
// is small — initialize, ping, tools/list, tools/call — and every dependency
// is a future packaging problem. Each POST carries one JSON-RPC message and
// is answered with one application/json body, which the transport permits in
// place of an event stream. There is no server-initiated stream, so GET is
// 405, as the transport specifies for servers that offer none.
//
// The server keeps no session state, so it issues no Mcp-Session-Id. That is
// also what Outpost's portal route needs: it drops Set-Cookie and passes only
// Mcp-Session-Id, and a stateless server needs neither.

// supportedProtocols, newest first. A client asking for one of these gets it
// back; anything else gets the newest, and the client decides whether to go on.
var supportedProtocols = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const maxMCPBody = 1 << 20

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "this MCP server takes POST only; it offers no event stream")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPBody+1))
	if err != nil {
		writeRPC(w, rpcResponse{ID: json.RawMessage("null"), Error: &rpcError{rpcParseError, "could not read body"}})
		return
	}
	if len(body) > maxMCPBody {
		writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		// Batching was removed from the transport in 2025-06-18.
		writeRPC(w, rpcResponse{ID: json.RawMessage("null"), Error: &rpcError{rpcInvalidRequest, "batches are not supported"}})
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, rpcResponse{ID: json.RawMessage("null"), Error: &rpcError{rpcParseError, "invalid JSON"}})
		return
	}

	// A notification, or a client's response to us, takes no reply.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, rpcResponse{ID: req.ID, Error: &rpcError{rpcInvalidRequest, "not a JSON-RPC 2.0 request"}})
		return
	}

	result, rerr := s.dispatch(req, user)
	writeRPC(w, rpcResponse{ID: req.ID, Result: result, Error: rerr})
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	resp.JSONRPC = "2.0"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatch(req rpcRequest, user string) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := supportedProtocols[0]
		if slices.Contains(supportedProtocols, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "shelf", "title": "shelf library", "version": s.svc.Version},
			"instructions": "shelf is an e-book library. Search with search_books (the query syntax is " +
				"bare words plus author:, series:, tag:, title: qualifiers, with and/or/not). " +
				"Book ids come from search results and are renumbered when the index is rebuilt, " +
				"so look one up just before using it.",
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			list = append(list, map[string]any{
				"name":        t.name,
				"title":       t.title,
				"description": t.description,
				"inputSchema": t.schema,
				"annotations": map[string]any{
					"readOnlyHint":    t.readOnly,
					"destructiveHint": t.destructive,
					"idempotentHint":  !t.notIdempotent,
					// A sync reaches out to a device on the network.
					"openWorldHint": t.openWorld,
				},
			})
		}
		return map[string]any{"tools": list}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{rpcInvalidParams, "params must be {name, arguments}"}
		}
		idx := slices.IndexFunc(tools, func(t tool) bool { return t.name == p.Name })
		if idx < 0 {
			return nil, &rpcError{rpcInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
		}
		args := p.Arguments
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		out, err := tools[idx].call(s.svc, args, user)
		return toolResult(out, err), nil
	}
	return nil, &rpcError{rpcMethodNotFound, fmt.Sprintf("method %q not found", req.Method)}
}

// toolResult reports a tool's outcome. Failures the model can act on — a bad
// argument, a missing book — come back as isError results it can read, not as
// protocol errors, which is how MCP asks tools to report them.
func toolResult(out any, err error) map[string]any {
	if err != nil {
		msg := err.Error()
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrBadRequest) && !errors.Is(err, ErrConflict) {
			msg = "internal error"
		}
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": msg}},
			"isError": true,
		}
	}
	text, _ := json.Marshal(out)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": out,
		"isError":           false,
	}
}

// tool is one MCP tool. Each is a thin decode-and-call over the Service, the
// same methods the REST routes use.
type tool struct {
	name, title, description string
	readOnly                 bool
	destructive              bool
	notIdempotent            bool
	openWorld                bool
	schema                   map[string]any
	call                     func(svc *Service, args json.RawMessage, user string) (any, error)
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	propUser   = map[string]any{"type": "string", "description": "whose reading progress to include; defaults to the caller or the configured user"}
	propBookID = map[string]any{"type": "integer", "minimum": 1, "description": "book id from search_books"}
	propShelf  = map[string]any{"type": "string", "description": "shelf name"}
)

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, badRequest("invalid arguments: %v", err)
	}
	return v, nil
}

var tools = []tool{
	{
		name: "search_books", title: "Search books", readOnly: true,
		description: "Search the library. Returns books with ids, metadata, AI genre when known, and reading progress.",
		schema: obj(map[string]any{
			"query":  map[string]any{"type": "string", "description": `e.g. earthsea, author:"le guin", tag:sci-fi and not tag:read; empty lists everything`},
			"genre":  map[string]any{"type": "string", "description": "only books of this genre (see list_values genres)"},
			"sort":   map[string]any{"type": "string", "enum": []string{"title", "author", "series", "size", "added", "modified", "path", "format"}},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": maxLimit},
			"offset": map[string]any{"type": "integer", "minimum": 0},
			"user":   propUser,
		}),
		call: func(svc *Service, args json.RawMessage, user string) (any, error) {
			a, err := decode[struct {
				Query, Genre, Sort, User string
				Limit, Offset            int
			}](args)
			if err != nil {
				return nil, err
			}
			return svc.Search(SearchParams{Query: a.Query, Genre: a.Genre, Sort: a.Sort,
				Limit: a.Limit, Offset: a.Offset, User: a.User}, user)
		},
	},
	{
		name: "get_book", title: "Get a book", readOnly: true,
		description: "Full metadata for one book by id.",
		schema:      obj(map[string]any{"id": propBookID, "user": propUser}, "id"),
		call: func(svc *Service, args json.RawMessage, user string) (any, error) {
			a, err := decode[struct {
				ID   int64
				User string
			}](args)
			if err != nil {
				return nil, err
			}
			return svc.Book(a.ID, a.User, user)
		},
	},
	{
		name: "list_values", title: "List authors, series, tags or genres", readOnly: true,
		description: "Every value of one browse dimension with how many books have it.",
		schema: obj(map[string]any{
			"dimension": map[string]any{"type": "string", "enum": []string{"authors", "series", "tags", "genres"}},
		}, "dimension"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct{ Dimension string }](args)
			if err != nil {
				return nil, err
			}
			var vals []Count
			switch a.Dimension {
			case "authors":
				vals, err = svc.Authors()
			case "series":
				vals, err = svc.Series()
			case "tags":
				vals, err = svc.Tags()
			case "genres":
				vals, err = svc.GenreCounts()
			default:
				return nil, badRequest("dimension must be authors, series, tags or genres")
			}
			return map[string]any{a.Dimension: vals}, err
		},
	},
	{
		name: "list_shelves", title: "List shelves", readOnly: true,
		description: "Every shelf: manual ones hold chosen books, query ones follow a saved search.",
		schema:      obj(map[string]any{}),
		call: func(svc *Service, _ json.RawMessage, _ string) (any, error) {
			res, err := svc.Shelves()
			return map[string]any{"shelves": res}, err
		},
	},
	{
		name: "get_shelf", title: "Get a shelf", readOnly: true,
		description: "One shelf and the books on it.",
		schema:      obj(map[string]any{"name": propShelf, "user": propUser}, "name"),
		call: func(svc *Service, args json.RawMessage, user string) (any, error) {
			a, err := decode[struct{ Name, User string }](args)
			if err != nil {
				return nil, err
			}
			return svc.Shelf(a.Name, a.User, user)
		},
	},
	{
		name: "add_to_shelf", title: "Add books to a shelf",
		description: "Add books, by id, to a manual shelf. The next `shelf sync` of that shelf sends them to the reader.",
		schema: obj(map[string]any{
			"name": propShelf,
			"ids":  map[string]any{"type": "array", "items": propBookID, "minItems": 1},
		}, "name", "ids"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct {
				Name string
				IDs  []int64
			}](args)
			if err != nil {
				return nil, err
			}
			return svc.AddToShelf(a.Name, a.IDs)
		},
	},
	{
		name: "remove_from_shelf", title: "Remove books from a shelf",
		description: "Take books, by id, off a manual shelf. The files stay in the library.",
		schema: obj(map[string]any{
			"name": propShelf,
			"ids":  map[string]any{"type": "array", "items": propBookID, "minItems": 1},
		}, "name", "ids"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct {
				Name string
				IDs  []int64
			}](args)
			if err != nil {
				return nil, err
			}
			return svc.RemoveFromShelf(a.Name, a.IDs)
		},
	},
	{
		name: "library_stats", title: "Library statistics", readOnly: true,
		description: "Counts of books, authors, series, tags, bytes, formats and genres.",
		schema:      obj(map[string]any{}),
		call: func(svc *Service, _ json.RawMessage, _ string) (any, error) {
			return svc.LibraryStats()
		},
	},
	{
		name: "reading_progress", title: "Reading progress", readOnly: true,
		description: "Books a reader has opened, most recently read first, with percent read.",
		schema:      obj(map[string]any{"user": propUser}),
		call: func(svc *Service, args json.RawMessage, user string) (any, error) {
			a, err := decode[struct{ User string }](args)
			if err != nil {
				return nil, err
			}
			return svc.ReadingProgress(a.User, user)
		},
	},
	{
		name: "update_book_metadata", title: "Edit a book's metadata",
		description: "Change an EPUB's title, authors, series, tags and other fields, written into the file itself. " +
			"Only the fields given change; an empty list clears authors or tags. The file does not move, so the " +
			"book keeps its place on the reader, but the next sync re-sends it with the new metadata.",
		schema: obj(map[string]any{
			"id":           propBookID,
			"title":        map[string]any{"type": "string"},
			"authors":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"series":       map[string]any{"type": "string", "description": "empty removes the series"},
			"series_index": map[string]any{"type": "number"},
			"language":     map[string]any{"type": "string", "description": "BCP 47, e.g. en"},
			"publisher":    map[string]any{"type": "string"},
			"pubdate":      map[string]any{"type": "string", "description": "e.g. 2020 or 2020-03-01"},
			"description":  map[string]any{"type": "string"},
			"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "id"),
		call: func(svc *Service, args json.RawMessage, user string) (any, error) {
			a, err := decode[struct {
				ID int64 `json:"id"`
				BookPatch
			}](args)
			if err != nil {
				return nil, err
			}
			return svc.UpdateBook(a.ID, a.BookPatch, user)
		},
	},
	{
		name: "create_shelf", title: "Create a shelf", notIdempotent: true,
		description: "Create a shelf. With a query it is a saved search whose books follow the query " +
			"(e.g. tag:sci-fi and not tag:read); without one it is a manual shelf for add_to_shelf.",
		schema: obj(map[string]any{
			"name":  propShelf,
			"query": map[string]any{"type": "string", "description": "saved search; omit for a manual shelf"},
		}, "name"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct{ Name, Query string }](args)
			if err != nil {
				return nil, err
			}
			return svc.CreateShelf(a.Name, a.Query)
		},
	},
	{
		name: "delete_shelf", title: "Delete a shelf", destructive: true,
		description: "Delete a shelf's definition. Its books stay in the library and on the reader.",
		schema:      obj(map[string]any{"name": propShelf}, "name"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct{ Name string }](args)
			if err != nil {
				return nil, err
			}
			if err := svc.DeleteShelf(a.Name); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": a.Name}, nil
		},
	},
	{
		name: "clear_reading_progress", title: "Clear a reading position", destructive: true,
		description: "Forget shelf's stored reading position for one book. The reader keeps its own position " +
			"and sends it again the next time the book is open with sync on; use this for stale entries.",
		schema: obj(map[string]any{"id": propBookID, "user": propUser}, "id"),
		call: func(svc *Service, args json.RawMessage, user string) (any, error) {
			a, err := decode[struct {
				ID   int64
				User string
			}](args)
			if err != nil {
				return nil, err
			}
			if err := svc.ClearProgress(a.ID, a.User, user); err != nil {
				return nil, err
			}
			return map[string]any{"cleared": a.ID}, nil
		},
	},
	{
		name: "list_devices", title: "List readers", readOnly: true,
		description: "The e-readers shelf syncs to, with when each was last synced and how many books it holds.",
		schema:      obj(map[string]any{}),
		call: func(svc *Service, _ json.RawMessage, _ string) (any, error) {
			if svc.Sync == nil {
				return nil, badRequest("syncing is not available")
			}
			res, err := svc.Sync.Devices()
			return map[string]any{"devices": res}, err
		},
	},
	{
		name: "add_device", title: "Add a reader", notIdempotent: true,
		description: "Add a Wi-Fi e-reader by its network address (the one its file transfer screen shows).",
		schema: obj(map[string]any{
			"name": map[string]any{"type": "string", "description": "a nickname, e.g. x4"},
			"host": map[string]any{"type": "string", "description": "IP address or hostname"},
			"root": map[string]any{"type": "string", "description": "folder on the reader; default /"},
		}, "name", "host"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			if svc.Sync == nil {
				return nil, badRequest("syncing is not available")
			}
			a, err := decode[struct{ Name, Host, Root string }](args)
			if err != nil {
				return nil, err
			}
			return svc.Sync.AddDevice(a.Name, a.Host, a.Root)
		},
	},
	{
		name: "sync_to_device", title: "Sync to a reader", notIdempotent: true, openWorld: true,
		description: "Start sending a shelf (or the whole library) to a reader. Runs as a job: poll get_sync_job. " +
			"dry_run defaults to true and only plans; show the user the plan, then run again with dry_run false. " +
			"prune also deletes books on the reader that are not in the shelf, so ask before setting it. " +
			"Books already on the reader never move, so nobody loses their place.",
		schema: obj(map[string]any{
			"device":  map[string]any{"type": "string", "description": "device name from list_devices; default device if omitted"},
			"shelf":   map[string]any{"type": "string", "description": "shelf to send; whole library if omitted"},
			"prune":   map[string]any{"type": "boolean"},
			"dry_run": map[string]any{"type": "boolean", "default": true},
		}),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct {
				Device, Shelf string
				Prune         bool
				DryRun        *bool `json:"dry_run"`
			}](args)
			if err != nil {
				return nil, err
			}
			dry := a.DryRun == nil || *a.DryRun
			return svc.StartSync(SyncRequest{Device: a.Device, Shelf: a.Shelf, Prune: a.Prune, DryRun: dry})
		},
	},
	{
		name: "get_sync_job", title: "Check a sync", readOnly: true,
		description: "A sync job's state: its plan, progress while running, and the result when done.",
		schema:      obj(map[string]any{"id": map[string]any{"type": "string"}}, "id"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			if svc.Sync == nil {
				return nil, badRequest("syncing is not available")
			}
			a, err := decode[struct{ ID string }](args)
			if err != nil {
				return nil, err
			}
			return svc.Sync.Job(a.ID)
		},
	},
	{
		name: "cancel_sync_job", title: "Cancel a sync",
		description: "Stop a running sync. Books already sent stay; the next sync carries on from there.",
		schema:      obj(map[string]any{"id": map[string]any{"type": "string"}}, "id"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			if svc.Sync == nil {
				return nil, badRequest("syncing is not available")
			}
			a, err := decode[struct{ ID string }](args)
			if err != nil {
				return nil, err
			}
			return svc.Sync.Cancel(a.ID)
		},
	},
	{
		name: "list_reader_accounts", title: "List reader accounts", readOnly: true,
		description: "The accounts e-readers sign in with, for progress sync and the OPDS catalog, with how many books each has a reading position in.",
		schema:      obj(map[string]any{}),
		call: func(svc *Service, _ json.RawMessage, _ string) (any, error) {
			res, err := svc.Accounts()
			return map[string]any{"accounts": res}, err
		},
	},
	{
		name: "create_reader_account", title: "Create a reader account", notIdempotent: true,
		description: "Create an account for an e-reader. Leave password out and shelf generates one that is easy to type " +
			"on the reader and safe to expose through Tailscale Funnel; it is returned once, in this result, and " +
			"cannot be shown again. The reader uses it for both the OPDS catalog (<server>/opds) and progress sync (<server>).",
		schema: obj(map[string]any{
			"name":     map[string]any{"type": "string", "description": "account name, e.g. x4"},
			"password": map[string]any{"type": "string", "minLength": 10, "description": "omit to generate one"},
		}, "name"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct{ Name, Password string }](args)
			if err != nil {
				return nil, err
			}
			return svc.CreateAccount(a.Name, a.Password)
		},
	},
	{
		name: "reset_reader_password", title: "Reset a reader account's password", notIdempotent: true,
		description: "Give a reader account a new password (generated when none is given, returned once). Reading positions are kept; the reader must sign in again.",
		schema: obj(map[string]any{
			"name":     map[string]any{"type": "string"},
			"password": map[string]any{"type": "string", "minLength": 10, "description": "omit to generate one"},
		}, "name"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct{ Name, Password string }](args)
			if err != nil {
				return nil, err
			}
			return svc.ResetPassword(a.Name, a.Password)
		},
	},
	{
		name: "delete_reader_account", title: "Delete a reader account", destructive: true,
		description: "Delete a reader account and every reading position it holds. Ask the user first: positions cannot be recovered.",
		schema:      obj(map[string]any{"name": map[string]any{"type": "string"}}, "name"),
		call: func(svc *Service, args json.RawMessage, _ string) (any, error) {
			a, err := decode[struct{ Name string }](args)
			if err != nil {
				return nil, err
			}
			if err := svc.DeleteAccount(a.Name); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": a.Name}, nil
		},
	},
}
