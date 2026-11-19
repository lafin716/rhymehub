import type { TaskState } from '../api/types';
import { stateLabel, stateTone } from '../lib/states';
import { Badge } from './ui';

export function StateBadge({ state }: { state: TaskState }) {
  return <Badge tone={stateTone(state)}>{stateLabel(state)}</Badge>;
}
