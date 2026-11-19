import { useEffect, useMemo, useState, type FormEvent } from "react";
import { RhymehubFeedback, RhymehubProvider, useRhymehubClient } from "@rhymehub/react";
import { readRhymehubConfig } from "./config";
import { addNote, clampCount, removeNote, stepCount, type Note } from "./lib/notes";

function Counter() {
  const [count, setCount] = useState(0);
  return (
    <section aria-labelledby="counter-heading">
      <h2 id="counter-heading">Counter</h2>
      <div className="row">
        <button type="button" onClick={() => setCount((c) => stepCount(c, -1))} aria-label="Decrement">
          -
        </button>
        <output aria-live="polite">{count}</output>
        <button type="button" onClick={() => setCount((c) => stepCount(c, 1))} aria-label="Increment">
          +
        </button>
        <button type="button" onClick={() => setCount(0)}>
          Reset
        </button>
      </div>
    </section>
  );
}

function Notes() {
  const [notes, setNotes] = useState<Note[]>([]);
  const [draft, setDraft] = useState("");

  function onAdd(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setNotes((current) => addNote(current, draft, crypto.randomUUID(), Date.now()));
    setDraft("");
  }

  return (
    <section aria-labelledby="notes-heading">
      <h2 id="notes-heading">Notes</h2>
      <form onSubmit={onAdd} className="row">
        <label htmlFor="note-input" className="sr-only">
          New note
        </label>
        <input id="note-input" value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="Write a note" />
        <button type="submit">Add</button>
      </form>
      <ul className="notes">
        {notes.map((note) => (
          <li key={note.id}>
            <span>{note.text}</span>
            <button type="button" onClick={() => setNotes((current) => removeNote(current, note.id))}>
              Remove
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Opt-in diagnostics: console warnings/errors and uncaught errors are kept in memory for reports. */
function CaptureDiagnostics() {
  const client = useRhymehubClient();
  useEffect(() => {
    const stopConsole = client.diagnostics.captureConsole({ levels: ["warn", "error"] });
    const stopErrors = client.diagnostics.captureErrors();
    return () => {
      stopConsole();
      stopErrors();
      client.diagnostics.dispose();
    };
  }, [client]);
  return null;
}

export function App() {
  const config = useMemo(() => readRhymehubConfig(import.meta.env), []);
  // Shown in the header so it is obvious which counter value a report refers to.
  const [visits] = useState(() => clampCount(Number(sessionStorage.getItem("demo-visits") ?? 0) + 1));
  useEffect(() => sessionStorage.setItem("demo-visits", String(visits)), [visits]);

  const body = (
    <>
      <Counter />
      <Notes />
    </>
  );

  return (
    <main className="app">
      <header>
        <h1>DroidPad demo</h1>
        <p className="muted">Session {visits}. A fake app that shows how rhymehub feedback is wired in.</p>
      </header>

      {config.ok ? (
        <RhymehubProvider options={config.options}>
          <CaptureDiagnostics />
          {body}
          <footer className="feedback-bar">
            <RhymehubFeedback title="Send feedback about DroidPad" triggerLabel="Send feedback" />
          </footer>
        </RhymehubProvider>
      ) : (
        <>
          {body}
          <p role="note" className="notice">
            Feedback is disabled. Set {config.missing.join(" and ")} in <code>.env.local</code> (see README).
          </p>
        </>
      )}
    </main>
  );
}
