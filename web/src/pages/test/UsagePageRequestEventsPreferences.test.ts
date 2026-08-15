import { describe, expect, it } from 'vitest';
import {
  normalizeRequestEventsPreferences,
} from '../UsagePage';
import { REQUEST_EVENT_COLUMN_IDS } from '@/components/usage/RequestEventsDetailsCard';

const LEGACY_V3_FULL_COLUMNS = [
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
  'cached_tokens',
  'cache_rate',
  'total_tokens',
  'total_cost',
];

const LEGACY_V2_FULL_COLUMNS = LEGACY_V3_FULL_COLUMNS.filter((columnId) => columnId !== 'model_alias');
const LEGACY_V1_FULL_COLUMNS = LEGACY_V2_FULL_COLUMNS.filter((columnId) => columnId !== 'service_tier');
const V7_FULL_COLUMNS = [
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
] as const;
const LEGACY_V4_FULL_COLUMNS = V7_FULL_COLUMNS.map((columnId) => (
  columnId === 'cache_read_rate' ? 'cache_rate' : columnId
));
const MERGED_V6_FULL_COLUMNS = V7_FULL_COLUMNS.flatMap((columnId) => (
  columnId === 'service_tier' ? [columnId, 'response_service_tier'] : [columnId]
));

describe('UsagePage request event cache column preferences', () => {
  it('upgrades a v5 full selection to all current columns', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 5,
      pageSize: 100,
      visibleColumnIds: V7_FULL_COLUMNS,
    });

    expect(preferences.version).toBe(8);
    expect(preferences.visibleColumnIds).toEqual(REQUEST_EVENT_COLUMN_IDS);
  });

  it('upgrades a v7 full selection with client metadata at the end', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 7,
      pageSize: 100,
      visibleColumnIds: V7_FULL_COLUMNS,
      columnOrder: V7_FULL_COLUMNS,
    });

    expect(preferences.version).toBe(8);
    expect(preferences.visibleColumnIds).toEqual(REQUEST_EVENT_COLUMN_IDS);
    expect(preferences.columnOrder.slice(-3)).toEqual([
      'client_ip',
      'x_forwarded_for',
      'user_agent',
    ]);
  });
  it('keeps a complete custom order independently from hidden columns', () => {
    const columnOrder = [
      'total_cost',
      'model',
      ...REQUEST_EVENT_COLUMN_IDS.filter((columnId) => columnId !== 'total_cost' && columnId !== 'model'),
    ];
    const preferences = normalizeRequestEventsPreferences({
      version: 8,
      pageSize: 100,
      visibleColumnIds: ['timestamp', 'model'],
      columnOrder,
    });

    expect(preferences.version).toBe(8);
    expect(preferences.visibleColumnIds).toEqual(['timestamp', 'model']);
    expect(preferences.columnOrder).toEqual(columnOrder);
  });

  it('keeps a custom prefix and appends columns missing from the saved order', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 8,
      pageSize: 100,
      visibleColumnIds: ['timestamp'],
      columnOrder: ['total_cost', 'timestamp', 'total_cost', 'not-a-column'],
    });

    expect(preferences.columnOrder).toEqual([
      'total_cost',
      'timestamp',
      ...REQUEST_EVENT_COLUMN_IDS.filter((columnId) => columnId !== 'total_cost' && columnId !== 'timestamp'),
    ]);
  });

  it('upgrades a v3 full selection to all current columns including cache write', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 3,
      pageSize: 100,
      visibleColumnIds: LEGACY_V3_FULL_COLUMNS,
    });

    expect(preferences.version).toBe(8);
    expect(preferences.visibleColumnIds).toEqual(REQUEST_EVENT_COLUMN_IDS);
    expect(preferences.visibleColumnIds).toContain('cache_read_tokens');
    expect(preferences.visibleColumnIds).toContain('cache_creation_tokens');
  });

  it('normalizes a merged v6 full selection back to the combined current column', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 6,
      pageSize: 100,
      visibleColumnIds: MERGED_V6_FULL_COLUMNS,
    });

    expect(preferences.version).toBe(8);
    expect(preferences.visibleColumnIds).toEqual(REQUEST_EVENT_COLUMN_IDS);
    expect(preferences.visibleColumnIds).not.toContain('response_service_tier' as never);
  });

  it('maps a merged v6 response-only selection to the combined Speed Mode column', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 6,
      pageSize: 100,
      visibleColumnIds: ['timestamp', 'response_service_tier', 'total_tokens'],
    });

    expect(preferences.visibleColumnIds).toEqual(['timestamp', 'service_tier', 'total_tokens']);
  });

  it('upgrades a v4 full selection and maps cache rate to cache read rate', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 4,
      pageSize: 100,
      visibleColumnIds: LEGACY_V4_FULL_COLUMNS,
    });

    expect(preferences.visibleColumnIds).toEqual(REQUEST_EVENT_COLUMN_IDS);
    expect(preferences.visibleColumnIds).toContain('cache_read_rate');
    expect(preferences.visibleColumnIds).not.toContain('cache_rate' as never);
  });

  it('maps a v3 custom cached column to cache read without adding cache write', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 3,
      pageSize: 100,
      visibleColumnIds: ['timestamp', 'cached_tokens', 'total_tokens'],
    });

    expect(preferences.visibleColumnIds).toEqual(['timestamp', 'cache_read_tokens', 'total_tokens']);
    expect(preferences.visibleColumnIds).not.toContain('cache_creation_tokens');
  });

  it('upgrades v2 and v1 full selections to all v4 columns', () => {
    for (const [version, visibleColumnIds] of [
      [2, LEGACY_V2_FULL_COLUMNS],
      [1, LEGACY_V1_FULL_COLUMNS],
    ] as const) {
      const preferences = normalizeRequestEventsPreferences({ version, pageSize: 100, visibleColumnIds });
      expect(preferences.visibleColumnIds).toEqual(REQUEST_EVENT_COLUMN_IDS);
    }
  });

  it('keeps legacy custom selections custom while mapping cached to cache read', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 2,
      pageSize: 100,
      visibleColumnIds: ['timestamp', 'cached_tokens', 'speed'],
    });

    expect(preferences.visibleColumnIds).toEqual(['timestamp', 'cache_read_tokens', 'speed']);
    expect(preferences.visibleColumnIds).not.toContain('model_alias');
    expect(preferences.visibleColumnIds).not.toContain('cache_creation_tokens');
  });

  it('maps an existing v4 custom cache rate column while preserving order', () => {
    const preferences = normalizeRequestEventsPreferences({
      version: 4,
      pageSize: 100,
      visibleColumnIds: ['total_tokens', 'cache_rate', 'cache_creation_tokens', 'cache_read_tokens', 'timestamp'],
    });

    expect(preferences.visibleColumnIds).toEqual([
      'total_tokens',
      'cache_read_rate',
      'cache_creation_tokens',
      'cache_read_tokens',
      'timestamp',
    ]);
  });
});
