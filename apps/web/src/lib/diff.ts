export type DiffLineKind = 'add' | 'del' | 'hunk' | 'ctx' | 'meta';

export interface DiffLine {
  kind: DiffLineKind;
  text: string;
}

export interface ParsedDiffFile {
  path: string;
  additions: number;
  deletions: number;
  lines: DiffLine[];
}

const DIFF_HEADER = /^diff --git a\/(.+?) b\/(.+)$/;

/**
 * Splits a unified git patch into per-file sections with classified lines. Lines are only
 * classified as content after the first hunk header, so `+++`/`---` file headers are not
 * mistaken for additions or deletions.
 */
export function parseUnifiedDiff(patch: string): ParsedDiffFile[] {
  const files: ParsedDiffFile[] = [];
  let current: ParsedDiffFile | null = null;
  let inHunk = false;

  const start = (path: string) => {
    current = { path, additions: 0, deletions: 0, lines: [] };
    files.push(current);
    inHunk = false;
  };

  for (const raw of patch.split('\n')) {
    const header = DIFF_HEADER.exec(raw);
    if (header) {
      start(header[2] ?? header[1] ?? '');
      current!.lines.push({ kind: 'meta', text: raw });
      continue;
    }
    if (!current) {
      // Patch without a git header (e.g. a bare hunk): collect under an unnamed file.
      if (raw.trim() === '') continue;
      start('(patch)');
    }
    const file = current!;

    if (!inHunk) {
      if (raw.startsWith('+++ ')) {
        const target = raw.slice(4).trim();
        if (target !== '/dev/null' && target.startsWith('b/')) file.path = target.slice(2);
        file.lines.push({ kind: 'meta', text: raw });
        continue;
      }
      if (raw.startsWith('@@')) {
        inHunk = true;
        file.lines.push({ kind: 'hunk', text: raw });
        continue;
      }
      if (raw !== '') file.lines.push({ kind: 'meta', text: raw });
      continue;
    }

    if (raw.startsWith('@@')) {
      file.lines.push({ kind: 'hunk', text: raw });
    } else if (raw.startsWith('+')) {
      file.additions += 1;
      file.lines.push({ kind: 'add', text: raw });
    } else if (raw.startsWith('-')) {
      file.deletions += 1;
      file.lines.push({ kind: 'del', text: raw });
    } else if (raw.startsWith('\\')) {
      file.lines.push({ kind: 'meta', text: raw });
    } else {
      file.lines.push({ kind: 'ctx', text: raw });
    }
  }

  return files;
}
