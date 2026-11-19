import { useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import type { RhymehubClient, SdkRequest, SdkRequestType } from "@rhymehub/sdk";
import { useClient } from "./provider";
import { useRhymehubRequest } from "./hooks";
import { styles } from "./styles";

const TYPE_LABELS: Record<SdkRequestType, string> = {
  bug: "Bug",
  feature: "Feature",
  improvement: "Improvement",
  refactor: "Refactor",
  performance: "Performance",
  security: "Security",
  docs: "Docs",
  release: "Release",
};

const DEFAULT_TYPES: SdkRequestType[] = ["bug", "feature", "improvement"];

export interface RhymehubFeedbackProps {
  /** Explicit client. Defaults to the nearest <RhymehubProvider>. */
  client?: RhymehubClient;
  /** Text on the trigger button. Default "Feedback". */
  triggerLabel?: string;
  /** Dialog heading. Default "Send feedback". */
  title?: string;
  /** Request types offered in the form. Default bug, feature, improvement. */
  types?: SdkRequestType[];
  /** Pre-selected type. Default: the first of `types`. */
  defaultType?: SdkRequestType;
  /** Called after the server accepts a request. */
  onSubmitted?: (request: SdkRequest) => void;
  /** Extra class on the trigger button (in addition to `rh-trigger`). */
  className?: string;
  /** How often the tracked request is refreshed. Default 3000 ms. */
  pollIntervalMs?: number;
}

type View = "form" | "track";

interface Draft {
  type: SdkRequestType;
  title: string;
  description: string;
}

function messageOf(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return String(err);
}

const FOCUSABLE =
  'button:not([disabled]), [href], input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * Self-contained feedback button and modal dialog. Submits a request through the SDK,
 * attaches any console/error diagnostics captured with `client.diagnostics`, then tracks
 * the request and lets the reporter answer the agent's questions.
 */
export function RhymehubFeedback(props: RhymehubFeedbackProps) {
  const client = useClient(props.client);
  const types = props.types?.length ? props.types : DEFAULT_TYPES;
  const emptyDraft = (): Draft => ({ type: props.defaultType ?? types[0], title: "", description: "" });

  const [open, setOpen] = useState(false);
  const [view, setView] = useState<View>("form");
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [trackedId, setTrackedId] = useState<string | null>(null);

  const triggerRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleFieldRef = useRef<HTMLInputElement>(null);
  const headingId = useId();
  const typeId = useId();
  const titleId = useId();
  const descriptionId = useId();

  useEffect(() => {
    if (!open) return;
    if (view === "form") titleFieldRef.current?.focus();
    else dialogRef.current?.focus();
  }, [open, view]);

  function close() {
    setOpen(false);
    setView("form");
    setFormError(null);
    triggerRef.current?.focus();
  }

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;
    if (draft.description.trim() === "") {
      setFormError("Please describe the problem or idea.");
      return;
    }
    setSubmitting(true);
    setFormError(null);
    try {
      const request = await client.requests.create({
        type: draft.type,
        title: draft.title.trim() || undefined,
        description: draft.description,
        diagnostics: client.diagnostics.snapshot(),
      });
      setTrackedId(request.id);
      setDraft(emptyDraft());
      setView("track");
      props.onSubmitted?.(request);
    } catch (err) {
      setFormError(messageOf(err));
    } finally {
      setSubmitting(false);
    }
  }

  function onDialogKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== "Tab" || !dialogRef.current) return;
    // Keep Tab focus inside the dialog (simple focus trap).
    const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE));
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === dialogRef.current)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const triggerClass = ["rh-trigger", props.className].filter(Boolean).join(" ");

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={triggerClass}
        style={styles.trigger}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => {
          setView("form");
          setOpen(true);
        }}
      >
        {props.triggerLabel ?? "Feedback"}
      </button>

      {open && (
        <div className="rh-overlay" style={styles.overlay} onMouseDown={(e) => e.target === e.currentTarget && close()}>
          <div
            ref={dialogRef}
            className="rh-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby={headingId}
            tabIndex={-1}
            style={styles.dialog}
            onKeyDown={onDialogKeyDown}
          >
            <h2 id={headingId} className="rh-heading" style={styles.heading}>
              {props.title ?? "Send feedback"}
            </h2>

            {view === "form" ? (
              <form className="rh-form" onSubmit={onSubmit} noValidate>
                <label className="rh-label" htmlFor={typeId} style={styles.label}>
                  Type
                </label>
                <select
                  id={typeId}
                  className="rh-field"
                  style={styles.field}
                  value={draft.type}
                  onChange={(e) => setDraft({ ...draft, type: e.target.value as SdkRequestType })}
                >
                  {types.map((t) => (
                    <option key={t} value={t}>
                      {TYPE_LABELS[t]}
                    </option>
                  ))}
                </select>

                <label className="rh-label" htmlFor={titleId} style={styles.label}>
                  Title <span style={styles.muted}>(optional)</span>
                </label>
                <input
                  id={titleId}
                  ref={titleFieldRef}
                  className="rh-field"
                  style={styles.field}
                  type="text"
                  maxLength={200}
                  value={draft.title}
                  onChange={(e) => setDraft({ ...draft, title: e.target.value })}
                />

                <label className="rh-label" htmlFor={descriptionId} style={styles.label}>
                  Details
                </label>
                <textarea
                  id={descriptionId}
                  className="rh-field"
                  style={{ ...styles.field, ...styles.textarea }}
                  value={draft.description}
                  aria-invalid={formError !== null}
                  aria-describedby={formError ? `${descriptionId}-error` : undefined}
                  onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                />

                <p className="rh-muted" style={styles.muted}>
                  Recent console messages and errors are attached automatically when capture is enabled.
                </p>

                {formError && (
                  <p id={`${descriptionId}-error`} role="alert" className="rh-error" style={styles.error}>
                    {formError}
                  </p>
                )}

                <div className="rh-actions" style={styles.row}>
                  <button type="button" className="rh-secondary" style={styles.secondary} onClick={close}>
                    Cancel
                  </button>
                  <button type="submit" className="rh-primary" style={styles.primary} disabled={submitting}>
                    {submitting ? "Sending..." : "Send"}
                  </button>
                </div>
              </form>
            ) : (
              <TrackPanel
                client={client}
                id={trackedId}
                intervalMs={props.pollIntervalMs}
                onSendAnother={() => {
                  setDraft(emptyDraft());
                  setView("form");
                }}
                onClose={close}
              />
            )}
          </div>
        </div>
      )}
    </>
  );
}

interface TrackPanelProps {
  client: RhymehubClient;
  id: string | null;
  intervalMs?: number;
  onSendAnother: () => void;
  onClose: () => void;
}

function TrackPanel({ client, id, intervalMs, onSendAnother, onClose }: TrackPanelProps) {
  const { request, error } = useRhymehubRequest(id, { client, intervalMs });
  const [answer, setAnswer] = useState("");
  const [answering, setAnswering] = useState(false);
  const [answerError, setAnswerError] = useState<string | null>(null);
  const [answeredQuestion, setAnsweredQuestion] = useState<string | null>(null);
  const answerId = useId();

  async function onAnswer(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!id || !request?.question || answering) return;
    if (answer.trim() === "") {
      setAnswerError("Please type an answer.");
      return;
    }
    setAnswering(true);
    setAnswerError(null);
    try {
      await client.requests.answer(id, answer);
      setAnsweredQuestion(request.question);
      setAnswer("");
    } catch (err) {
      setAnswerError(messageOf(err));
    } finally {
      setAnswering(false);
    }
  }

  if (!request) {
    return (
      <div className="rh-track" role="status">
        <p style={styles.muted}>{error ? `Could not load request: ${messageOf(error)}` : "Sending..."}</p>
      </div>
    );
  }

  const awaitingAnswer =
    request.status === "waiting_input" && !!request.question && request.question !== answeredQuestion;
  const percent = Math.max(0, Math.min(100, request.progress.percent));

  return (
    <div className="rh-track">
      <p role="status" className="rh-status" style={styles.muted}>
        Request {request.id}: <strong>{request.progress.label}</strong>
      </p>
      <div style={styles.progress} aria-hidden="true">
        <div style={{ ...styles.progressBar, width: `${percent}%` }} />
      </div>

      {awaitingAnswer && (
        <form className="rh-answer" onSubmit={onAnswer} noValidate>
          <label className="rh-label" htmlFor={answerId} style={styles.label}>
            The agent asks: {request.question}
          </label>
          <textarea
            id={answerId}
            className="rh-field"
            style={{ ...styles.field, ...styles.textarea, minHeight: 70 }}
            value={answer}
            onChange={(e) => setAnswer(e.target.value)}
          />
          {answerError && (
            <p role="alert" className="rh-error" style={styles.error}>
              {answerError}
            </p>
          )}
          <div style={styles.row}>
            <button type="submit" className="rh-primary" style={styles.primary} disabled={answering}>
              {answering ? "Sending..." : "Send answer"}
            </button>
          </div>
        </form>
      )}

      {request.result?.summary && <p style={styles.muted}>{request.result.summary}</p>}

      {error !== undefined && (
        <p role="alert" className="rh-error" style={styles.error}>
          {messageOf(error)}
        </p>
      )}

      <div className="rh-actions" style={styles.row}>
        <button type="button" className="rh-secondary" style={styles.secondary} onClick={onSendAnother}>
          Send another
        </button>
        <button type="button" className="rh-primary" style={styles.primary} onClick={onClose}>
          Close
        </button>
      </div>
    </div>
  );
}
