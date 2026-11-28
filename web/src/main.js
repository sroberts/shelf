import { mount } from 'svelte';
import './styles/tokens.css';
import './styles/base.css';
import App from './App.svelte';
import { registerWorker } from './lib/pwa.js';

// Cast: without strictNullChecks, svelte's MountOptions resolves to the
// custom-renderer branch and asks for a renderer the DOM does not need.
mount(App, /** @type {any} */ ({ target: document.getElementById('app') }));
registerWorker();
