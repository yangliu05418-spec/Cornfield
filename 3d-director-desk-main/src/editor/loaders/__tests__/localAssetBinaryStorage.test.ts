import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createLocalAssetBinaryStorage,
  createStoredAssetUrl,
  getStoredAssetKey,
  type LocalAssetBinaryBackend,
  type LocalAssetBinaryRecord,
} from "../localAssetBinaryStorage";
import { setDirectorTenantScope } from "../../io/tenantScope";

const TENANT_A = "5d4427a8-57e4-4f37-bf15-caf6d2fc5e64";
const TENANT_B = "84d9b9f4-b8c3-42e1-8875-3ad1d094bc65";

function createMemoryBackend() {
  const records = new Map<string, LocalAssetBinaryRecord>();
  const backend: LocalAssetBinaryBackend = {
    put: vi.fn(async (record) => { records.set(record.key, record); }),
    get: vi.fn(async (key) => records.get(key) ?? null),
    delete: vi.fn(async (key) => { records.delete(key); }),
  };
  return { backend, records };
}

describe("local asset binary storage", () => {
  afterEach(() => {
    setDirectorTenantScope(null);
    window.history.replaceState({}, "", "/");
  });

  it("stores model bytes outside the project JSON and restores them by stable key", async () => {
    const { backend } = createMemoryBackend();
    const storage = createLocalAssetBinaryStorage(backend);
    const file = new File(["model-bytes"], "actor.glb", { type: "model/gltf-binary" });

    const saved = await storage.save(file, "actor-key");
    const restored = await storage.read("actor-key");

    expect(saved).toMatchObject({ key: "actor-key", fileName: "actor.glb", byteLength: 11 });
    expect(restored?.blob).toBe(file);
    expect(createStoredAssetUrl(saved.key)).toBe("director-asset://local/actor-key");
    expect(getStoredAssetKey(createStoredAssetUrl(saved.key))).toBe("actor-key");
  });

  it("removes one stored binary without touching other assets", async () => {
    const { backend, records } = createMemoryBackend();
    const storage = createLocalAssetBinaryStorage(backend);
    await storage.save(new File(["a"], "a.fbx"), "a");
    await storage.save(new File(["b"], "b.fbx"), "b");

    await storage.remove("a");
    expect(records.has("a")).toBe(false);
    expect(records.has("b")).toBe(true);
  });

  it("reports unavailable storage clearly instead of silently losing a file", async () => {
    const storage = createLocalAssetBinaryStorage(null);
    expect(storage.isAvailable).toBe(false);
    await expect(storage.save(new File(["x"], "x.fbx"))).rejects.toThrow("当前浏览器不支持大型本地模型存储");
  });

  it("prevents another tenant from reading or deleting a stored binary", async () => {
    const { backend, records } = createMemoryBackend();
    const storage = createLocalAssetBinaryStorage(backend);
    const file = new File(["private-model"], "private.glb", { type: "model/gltf-binary" });

    setDirectorTenantScope(TENANT_A);
    const saved = await storage.save(file, "asset-a");
    expect(saved.key).toBe(`user:${TENANT_A}:asset-a`);

    setDirectorTenantScope(TENANT_B);
    await expect(storage.read(saved.key)).resolves.toBeNull();
    await storage.remove(saved.key);
    expect(records.has(saved.key)).toBe(true);
    await expect(storage.save(file, saved.key)).rejects.toThrow("本地模型不属于当前账户");
  });

  it("does not access local binaries in embedded mode before a tenant session", async () => {
    const { backend } = createMemoryBackend();
    const storage = createLocalAssetBinaryStorage(backend);
    const file = new File(["private-model"], "private.glb", { type: "model/gltf-binary" });
    window.history.replaceState({}, "", "/?embedded=1");

    await expect(storage.save(file, "asset-a")).rejects.toThrow("导演台尚未建立安全会话");
    await expect(storage.read("asset-a")).resolves.toBeNull();
    await storage.remove("asset-a");

    expect(backend.put).not.toHaveBeenCalled();
    expect(backend.get).not.toHaveBeenCalled();
    expect(backend.delete).not.toHaveBeenCalled();
  });
});
