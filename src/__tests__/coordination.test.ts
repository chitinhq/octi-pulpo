import { describe, it, expect, beforeEach } from 'vitest';
import MockRedis from 'ioredis-mock';
import type Redis from 'ioredis';
import { CoordinationEngine } from '../coordination.js';
import type { OctiConfig } from '../types.js';

const config: OctiConfig = { redisUrl: 'redis://localhost:6379', port: 3000, namespace: 'test' };
const redis = new MockRedis() as unknown as Redis;
const engine = new CoordinationEngine(config, redis);

beforeEach(async () => {
  await redis.flushall();
});

describe('CoordinationEngine', () => {
  describe('claim()', () => {
    it('returns a claim with correct shape', async () => {
      const claim = await engine.claim('agent-a', 'build the widget', 300);
      expect(claim.agentId).toBe('agent-a');
      expect(claim.task).toBe('build the widget');
      expect(claim.ttlSeconds).toBe(300);
      expect(claim.claimId).toContain('agent-a');
      expect(claim.claimedAt).toBeTruthy();
    });

    it('rejects a second agent trying to claim the same task', async () => {
      await engine.claim('agent-a', 'write the migration', 900);
      await expect(engine.claim('agent-b', 'write the migration', 900))
        .rejects.toThrow('already claimed by agent-a');
    });

    it('allows the same agent to renew its own claim', async () => {
      await engine.claim('agent-a', 'write the migration', 900);
      const renewed = await engine.claim('agent-a', 'write the migration', 900);
      expect(renewed.agentId).toBe('agent-a');
    });

    it('treats different tasks as independent claims', async () => {
      const c1 = await engine.claim('agent-a', 'task-one', 300);
      const c2 = await engine.claim('agent-b', 'task-two', 300);
      expect(c1.task).toBe('task-one');
      expect(c2.task).toBe('task-two');
    });
  });

  describe('activeClaims()', () => {
    it('lists all active claims', async () => {
      await engine.claim('agent-a', 'task alpha', 900);
      await engine.claim('agent-b', 'task beta', 900);
      const claims = await engine.activeClaims();
      const tasks = claims.map((c) => c.task);
      expect(tasks).toContain('task alpha');
      expect(tasks).toContain('task beta');
    });

    it('returns empty array when nothing is claimed', async () => {
      const claims = await engine.activeClaims();
      expect(claims).toEqual([]);
    });
  });

  describe('signal()', () => {
    it('stores signals that can be retrieved via recentSignals()', async () => {
      await engine.signal('agent-a', 'completed', 'finished the auth module');
      const signals = await engine.recentSignals();
      expect(signals).toHaveLength(1);
      expect(signals[0].agentId).toBe('agent-a');
      expect(signals[0].type).toBe('completed');
      expect(signals[0].payload).toBe('finished the auth module');
    });

    it('returns signals in reverse chronological order', async () => {
      await engine.signal('agent-a', 'heartbeat', 'alive-1');
      await engine.signal('agent-b', 'blocked', 'waiting on db migration');
      const signals = await engine.recentSignals(10);
      expect(signals[0].payload).toBe('waiting on db migration');
      expect(signals[1].payload).toBe('alive-1');
    });

    it('respects the limit parameter', async () => {
      for (let i = 0; i < 10; i++) {
        await engine.signal('agent-a', 'heartbeat', `ping-${i}`);
      }
      const signals = await engine.recentSignals(3);
      expect(signals).toHaveLength(3);
    });

    it('trims the signal log to 500 entries', async () => {
      for (let i = 0; i < 505; i++) {
        await engine.signal('agent-a', 'heartbeat', `ping-${i}`);
      }
      const signals = await engine.recentSignals(600);
      expect(signals.length).toBeLessThanOrEqual(500);
    });
  });
});
