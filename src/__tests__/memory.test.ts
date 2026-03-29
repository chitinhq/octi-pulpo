import { describe, it, expect, beforeEach } from 'vitest';
import MockRedis from 'ioredis-mock';
import type Redis from 'ioredis';
import { MemoryStore } from '../memory.js';
import type { OctiConfig } from '../types.js';

const config: OctiConfig = { redisUrl: 'redis://localhost:6379', port: 3000, namespace: 'test' };
const redis = new MockRedis() as unknown as Redis;
const store = new MemoryStore(config, redis);

beforeEach(async () => {
  await redis.flushall();
});

describe('MemoryStore', () => {
  describe('store()', () => {
    it('returns a unique memory ID', async () => {
      const id1 = await store.store('agent-a', 'learned something', ['bootstrap']);
      const id2 = await store.store('agent-a', 'learned something else', ['redis']);
      expect(id1).toBeTruthy();
      expect(id2).toBeTruthy();
      expect(id1).not.toBe(id2);
    });

    it('persists the entry so it can be recalled', async () => {
      await store.store('agent-a', 'pnpm needs --shamefully-hoist in monorepo', ['pnpm', 'monorepo']);
      const results = await store.recall('pnpm', 5);
      expect(results).toHaveLength(1);
      expect(results[0].content).toContain('pnpm needs');
      expect(results[0].agentId).toBe('agent-a');
    });

    it('records the stored topics on the returned entry', async () => {
      await store.store('agent-b', 'redis sorted sets preserve insertion order', ['redis', 'data-structures']);
      const results = await store.recall('redis', 5);
      expect(results[0].topics).toEqual(expect.arrayContaining(['redis', 'data-structures']));
    });
  });

  describe('recall()', () => {
    it('matches content keywords case-insensitively', async () => {
      await store.store('agent-a', 'Worktree isolation prevents branch conflicts', ['git', 'worktree']);
      const results = await store.recall('worktree', 5);
      expect(results.length).toBeGreaterThan(0);
    });

    it('matches topic tags', async () => {
      await store.store('agent-a', 'some observation', ['ci-pipeline', 'builds']);
      const results = await store.recall('ci-pipeline', 5);
      expect(results).toHaveLength(1);
    });

    it('returns empty array when nothing matches', async () => {
      await store.store('agent-a', 'about redis', ['redis']);
      const results = await store.recall('qdrant vector embeddings', 5);
      expect(results).toEqual([]);
    });

    it('respects the limit parameter', async () => {
      for (let i = 0; i < 10; i++) {
        await store.store('agent-a', `redis fact ${i}`, ['redis']);
      }
      const results = await store.recall('redis', 3);
      expect(results).toHaveLength(3);
    });

    it('returns memories from multiple agents', async () => {
      await store.store('agent-alpha', 'alpha knows about redis', ['redis']);
      await store.store('agent-beta', 'beta also knows about redis', ['redis']);
      const results = await store.recall('redis', 10);
      const agentIds = results.map((r) => r.agentId);
      expect(agentIds).toContain('agent-alpha');
      expect(agentIds).toContain('agent-beta');
    });
  });
});
