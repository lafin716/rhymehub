import { describe, expect, it } from "vitest";
import {
  COUNTER_MAX,
  MAX_NOTE_LENGTH,
  addNote,
  clampCount,
  normalizeNoteText,
  removeNote,
  stepCount,
  type Note,
} from "./notes";

describe("clampCount / stepCount", () => {
  it("keeps the counter inside its range", () => {
    expect(clampCount(-5)).toBe(0);
    expect(clampCount(COUNTER_MAX + 10)).toBe(COUNTER_MAX);
    expect(clampCount(7.9)).toBe(7);
  });

  it("treats non-finite values as the minimum", () => {
    expect(clampCount(Number.NaN)).toBe(0);
    expect(clampCount(Number.POSITIVE_INFINITY)).toBe(0);
  });

  it("steps and stops at the bounds", () => {
    expect(stepCount(0, -1)).toBe(0);
    expect(stepCount(COUNTER_MAX, 1)).toBe(COUNTER_MAX);
    expect(stepCount(4, 1)).toBe(5);
    expect(stepCount(4, -3)).toBe(1);
  });
});

describe("normalizeNoteText", () => {
  it("returns null for blank input", () => {
    expect(normalizeNoteText("   \n\t ")).toBeNull();
  });

  it("collapses whitespace and trims", () => {
    expect(normalizeNoteText("  buy   milk \n now ")).toBe("buy milk now");
  });

  it("caps the length", () => {
    expect(normalizeNoteText("x".repeat(MAX_NOTE_LENGTH + 50))).toHaveLength(MAX_NOTE_LENGTH);
  });
});

describe("addNote / removeNote", () => {
  const existing: Note[] = [{ id: "a", text: "first", createdAt: 1 }];

  it("prepends a normalized note", () => {
    const next = addNote(existing, "  second  one ", "b", 2);
    expect(next.map((n) => n.id)).toEqual(["b", "a"]);
    expect(next[0]).toEqual({ id: "b", text: "second one", createdAt: 2 });
  });

  it("ignores blank input and returns the same array", () => {
    expect(addNote(existing, "   ", "b", 2)).toBe(existing);
  });

  it("removes by id without mutating the input", () => {
    const next = removeNote(existing, "a");
    expect(next).toEqual([]);
    expect(existing).toHaveLength(1);
  });
});
