export const REQUEST_EVENT_COLUMN_IDS = [
  'timestamp',
  'api_key',
  'source',
  'model',
  'model_alias',
  'reasoning_effort',
  'service_tier',
  'result',
  'request_type',
  'endpoint',
  'ttft',
  'latency',
  'speed',
  'input_tokens',
  'output_tokens',
  'reasoning_tokens',
  'cache_read_tokens',
  'cache_creation_tokens',
  'cache_read_rate',
  'total_tokens',
  'total_cost',
  'client_ip',
  'x_forwarded_for',
  'user_agent',
] as const;

export type RequestEventColumnId = typeof REQUEST_EVENT_COLUMN_IDS[number];

const REQUEST_EVENT_COLUMN_ID_SET: ReadonlySet<string> = new Set(REQUEST_EVENT_COLUMN_IDS);

export const normalizeRequestEventVisibleColumnIds = (
  columnIds: readonly RequestEventColumnId[],
  availableColumnIds: readonly RequestEventColumnId[] = REQUEST_EVENT_COLUMN_IDS,
): RequestEventColumnId[] => {
  const availableSet = new Set<RequestEventColumnId>(availableColumnIds);
  const seen = new Set<RequestEventColumnId>();
  const normalized = columnIds.filter((columnId) => {
    if (!REQUEST_EVENT_COLUMN_ID_SET.has(columnId) || !availableSet.has(columnId) || seen.has(columnId)) {
      return false;
    }
    seen.add(columnId);
    return true;
  });

  return normalized.length > 0 ? normalized : [...availableColumnIds];
};

export const normalizeRequestEventColumnOrder = (
  columnIds: readonly RequestEventColumnId[],
  availableColumnIds: readonly RequestEventColumnId[] = REQUEST_EVENT_COLUMN_IDS,
): RequestEventColumnId[] => {
  const availableSet = new Set<RequestEventColumnId>(availableColumnIds);
  const seen = new Set<RequestEventColumnId>();
  const normalized = columnIds.filter((columnId) => {
    if (!REQUEST_EVENT_COLUMN_ID_SET.has(columnId) || !availableSet.has(columnId) || seen.has(columnId)) {
      return false;
    }
    seen.add(columnId);
    return true;
  });

  for (const columnId of availableColumnIds) {
    if (!seen.has(columnId)) {
      normalized.push(columnId);
    }
  }
  return normalized;
};

export const toggleRequestEventColumnId = (
  columnIds: readonly RequestEventColumnId[],
  columnId: RequestEventColumnId,
  availableColumnIds: readonly RequestEventColumnId[] = REQUEST_EVENT_COLUMN_IDS,
): RequestEventColumnId[] => {
  const normalized = normalizeRequestEventVisibleColumnIds(columnIds, availableColumnIds);
  if (!availableColumnIds.includes(columnId)) {
    return normalized;
  }
  if (normalized.includes(columnId)) {
    return normalized.length <= 1 ? normalized : normalized.filter((currentColumnId) => currentColumnId !== columnId);
  }
  return availableColumnIds.filter((currentColumnId) => normalized.includes(currentColumnId) || currentColumnId === columnId);
};

export const moveRequestEventColumnId = <T extends readonly RequestEventColumnId[]>(
  columnIds: T,
  columnId: RequestEventColumnId,
  targetIndex: number,
): T | RequestEventColumnId[] => {
  const currentIndex = columnIds.indexOf(columnId);
  if (currentIndex < 0 || targetIndex < 0 || targetIndex >= columnIds.length || currentIndex === targetIndex) {
    return columnIds;
  }

  const nextColumnIds = [...columnIds];
  nextColumnIds.splice(currentIndex, 1);
  nextColumnIds.splice(targetIndex, 0, columnId);
  return nextColumnIds;
};
