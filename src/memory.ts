import Redis from 'ioredis';
import type { OctiConfig, SwarmMemory } from './types.js';

/** Shared swarm memory — Redis-backed with optional vector search. */
export class MemoryStore {
  private redis: Redis;
  private ns: string;

  constructor(config: OctiConfig, redis?: Redis) {
    this.redis = redis ?? new Redis(config.redisUrl);
    this.ns = config.namespace;
  }

  /** Store a memory entry. Returns the memory ID. */
  async store(agentId: string, content: string, topics: string[]): Promise<string> {
    const id = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const entry: SwarmMemory = {
      id,
      agentId,
      content,
      topics,
      storedAt: new Date().toISOString(),
    };
    // Store in Redis sorted set (by timestamp) and per-topic sets
    const key = `${this.ns}:memories`;
    await this.redis.zadd(key, Date.now(), JSON.stringify(entry));
    for (const topic of topics) {
      await this.redis.sadd(`${this.ns}:topic:${topic}`, id);
    }
    // Store the full entry by ID for direct lookup
    await this.redis.set(`${this.ns}:memory:${id}`, JSON.stringify(entry), 'EX', 86400 * 30);
    return id;
  }

  /** Recall memories matching a query. Currently keyword-based; vector search coming. */
  async recall(query: string, limit: number): Promise<SwarmMemory[]> {
    const key = `${this.ns}:memories`;
    // Get recent memories and filter by keyword match
    const raw = await this.redis.zrevrange(key, 0, 200);
    const queryLower = query.toLowerCase();
    const keywords = queryLower.split(/\s+/);

    const matches = raw
      .map((r) => JSON.parse(r) as SwarmMemory)
      .filter((m) => {
        const text = `${m.content} ${m.topics.join(' ')}`.toLowerCase();
        return keywords.some((kw) => text.includes(kw));
      })
      .slice(0, limit);

    return matches;
  }

  async close(): Promise<void> {
    await this.redis.quit();
  }
}
