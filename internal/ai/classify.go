package ai

import (
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/library"
)

// Genres are the labels the classifier chooses between. A fixed, short list
// with a description each: that is what a System 1 "choice" needs, and it
// keeps the question portable across every provider (spec §15).
var Genres = map[string]string{
	"fantasy":         "magic, invented worlds, myth and legend",
	"science-fiction": "speculative technology, space, the future, alternate science",
	"mystery":         "crime, detectives, investigations, thrillers and suspense",
	"romance":         "a love story is the central plot",
	"horror":          "fear, the supernatural, dread",
	"literary":        "literary or general fiction not in a genre above",
	"historical":      "fiction set in a real historical period",
	"biography":       "the life of a real person, memoir, autobiography",
	"history":         "non-fiction about past events and societies",
	"science":         "non-fiction about science, nature, mathematics or technology",
	"self-help":       "advice, productivity, psychology for everyday life, business",
	"children":        "written for children or young adults",
	"other":           "anything that fits none of the above",
}

// minConfidence is how sure the model must be before a genre is recorded.
// Below it the answer is "don't know", which is better left blank than shown.
const minConfidence = 0.5

// maxRetryWait bounds a single Retry-After, so a provider asking for an hour
// does not park the pass that long; the next Run picks the book up instead.
const maxRetryWait = 2 * time.Minute

// maxRateLimitRetries bounds how often one book is retried after a 429.
const maxRateLimitRetries = 3

// descriptionLimit keeps the state small: the description decides the genre
// within a few sentences, and every token sent costs the operator.
const descriptionLimit = 2000

// Classifier assigns each book a genre with the System 1 model.
type Classifier struct {
	Client *Client
	Store  *Store
	DB     *library.DB
	Log    *slog.Logger

	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error
}

// Run makes one serial pass over every book not yet classified.
//
// It never fails the caller for an AI problem: a model that is unavailable,
// not given to shelf, or rejecting the token ends the pass quietly, to be
// tried again on the next Run. Only an index or store error is returned.
func (c *Classifier) Run(ctx context.Context) error {
	if c.Client == nil || c.Store == nil || c.DB == nil {
		return nil
	}
	log := c.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	books, err := c.DB.All()
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, b := range books {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if b.SHA256 == "" || seen[b.SHA256] {
			continue
		}
		seen[b.SHA256] = true

		done, err := c.Store.Classified(b.SHA256)
		if err != nil {
			return err
		}
		if done {
			continue
		}

		stop, err := c.classify(ctx, log, b)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// classify asks about one book. stop is true when the pass should end.
func (c *Classifier) classify(ctx context.Context, log *slog.Logger, b *library.Book) (stop bool, err error) {
	state := bookState(b)
	questions := map[string]Question{
		"genre": {
			Type:         "choice",
			Instructions: "Which genre best describes this book?",
			Criteria:     Genres,
		},
		"fiction": {
			Type:         "noul",
			Instructions: "Is this book a work of fiction?",
		},
	}

	for attempt := 0; ; attempt++ {
		answers, model, err := c.Client.System1(ctx, state, questions)
		switch {
		case err == nil:
			genre, fiction := interpret(answers)
			log.Debug("classified", "title", b.DisplayTitle(), "genre", genre, "model", model)
			return false, c.Store.SetGenre(b.SHA256, genre, fiction, model)

		case ctx.Err() != nil:
			return true, nil

		case Unauthorized(err), Forbidden(err):
			// Not something more requests fix; the operator has to act.
			log.Warn("AI genre classification stopped: System 1 model not available to shelf", "err", err)
			return true, nil

		case Unavailable(err):
			log.Info("AI genre classification paused: System 1 model unavailable", "err", err)
			return true, nil

		case RateLimited(err) && attempt < maxRateLimitRetries:
			wait := retryWait(err)
			log.Info("AI rate limited; waiting", "wait", wait.String())
			if c.wait(ctx, wait) != nil {
				return true, nil
			}
			continue

		case RateLimited(err):
			return true, nil

		case statusOf(err) == 0:
			// A transport failure, such as Outpost restarting. Treat it as an
			// outage rather than as this book's fault.
			log.Info("AI genre classification paused", "err", err)
			return true, nil

		default:
			// The request itself was refused (400 and the like). Recording a
			// blank answer means one odd book cannot be retried forever.
			log.Warn("AI could not classify book", "title", b.DisplayTitle(), "err", err)
			return false, c.Store.SetGenre(b.SHA256, "", nil, "")
		}
	}
}

func retryWait(err error) time.Duration {
	var d time.Duration
	var e *APIError
	if errors.As(err, &e) {
		d = e.RetryAfter
	}
	if d <= 0 {
		d = 10 * time.Second
	}
	if d > maxRetryWait {
		d = maxRetryWait
	}
	return d
}

func (c *Classifier) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// interpret turns answers into a genre, blank unless confident, and the
// probability the book is fiction.
func interpret(answers map[string]Answer) (string, *float64) {
	var fiction *float64
	if a, ok := answers["fiction"]; ok && a.Type == "noul" {
		v := a.Noul
		fiction = &v
	}

	a, ok := answers["genre"]
	if !ok || a.Type != "choice" || a.Choice == "" {
		return "", fiction
	}
	if _, known := Genres[a.Choice]; !known {
		return "", fiction
	}
	conf := a.Confidence
	if p, ok := a.Probabilities[a.Choice]; ok && conf == 0 {
		conf = p
	}
	if conf < minConfidence {
		return "", fiction
	}
	return a.Choice, fiction
}

type bookFacts struct {
	Title       string   `json:"title"`
	Authors     []string `json:"authors,omitempty"`
	Series      string   `json:"series,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
}

func bookState(b *library.Book) bookFacts {
	s := bookFacts{Title: b.DisplayTitle(), Authors: b.Authors, Series: b.Series, Tags: b.Tags}
	if b.Format == library.FormatEPUB {
		s.Description = description(b.Path)
	}
	return s
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// description reads an EPUB's dc:description as plain text, or "" when it
// cannot. Descriptions are frequently HTML; a crude strip is enough for a
// model to read.
func description(path string) string {
	f, err := epub.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	d := tagPattern.ReplaceAllString(f.Metadata().Description, " ")
	d = strings.Join(strings.Fields(html.UnescapeString(d)), " ")
	if len(d) > descriptionLimit {
		d = d[:descriptionLimit]
		for !utf8.ValidString(d) {
			d = d[:len(d)-1]
		}
	}
	return d
}
