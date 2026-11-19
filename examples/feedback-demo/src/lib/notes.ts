// Pure state helpers for the demo app. No React or DOM imports so they are easy to test.

export const MAX_NOTE_LENGTH = 280;
export const COUNTER_MIN = 0;
export const COUNTER_MAX = 999;

export interface Note {
  id: string;
  text: string;
  createdAt: number;
}

/** Clamp to the counter range and drop NaN/Infinity to the minimum. */
export function clampCount(value: number, min = COUNTER_MIN, max = COUNTER_MAX): number {
  if (!Number.isFinite(value)) return min;
  return Math.min(max, Math.max(min, Math.trunc(value)));
}

export function stepCount(current: number, delta: number): number {
  return clampCount(current + delta);
}

/** Collapse whitespace, trim, and cap length. Returns null for empty input. */
export function normalizeNoteText(raw: string): string | null {
  const text = raw.replace(/\s+/g, " ").trim();
  if (text === "") return null;
  return text.slice(0, MAX_NOTE_LENGTH);
}

/** Newest notes first. Returns the same array when the input is blank. */
export function addNote(notes: Note[], raw: string, id: string, now: number): Note[] {
  const text = normalizeNoteText(raw);
  if (text === null) return notes;
  return [{ id, text, createdAt: now }, ...notes];
}

export function removeNote(notes: Note[], id: string): Note[] {
  return notes.filter((note) => note.id !== id);
}
