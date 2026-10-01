import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { backups, orgs, tables } from "../api";
import { deferEffect } from "../defer-effect";

describe("authenticated request identity", () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({}) });
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => { vi.unstubAllGlobals(); fetchMock.mockClear(); });

  it("keeps a crafted invitation token inside its own route segment", async () => {
    const token = "../../projects/project/auth/users/user/ban#";
    await orgs.acceptInvite("credential", token);
    const url = new URL(fetchMock.mock.calls[0][0]);
    expect(url.pathname).toBe(`/platform/orgs/invites/${encodeURIComponent(token)}/accept`);
    expect(url.hash).toBe("");
  });

  it.each(["victim&junk=1", "key#fragment", "plus+space", "%2Fpercent", "文字%+&#"]) ("preserves the exact identity of an edited/deleted row: %s", async (key) => {
    await tables.updateRow("credential", "project", "rows", "id", key, { content: "changed" });
    await tables.deleteRow("credential", "project", "rows", "id", key);
    for (const [address] of fetchMock.mock.calls) {
      const url = new URL(address);
      expect(url.searchParams.get("pk_value")).toBe(key);
      expect([...url.searchParams.keys()]).toEqual(["schema", "pk_column", "pk_value"]);
      expect(url.hash).toBe("");
    }
  });

  it("toggles the selected organization's backup settings", async () => {
    await backups.toggleEnabled("credential", false, "fixture-password", "shared-org");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).org_id).toBe("shared-org");
  });
});

describe("deferred external work", () => {
  it("cancels obsolete work before it starts and cleans up started work", async () => {
    const work = vi.fn();
    const cancel = deferEffect(work);
    cancel();
    await Promise.resolve();
    expect(work).not.toHaveBeenCalled();
    const cleanup = vi.fn();
    const stop = deferEffect(() => cleanup);
    await Promise.resolve();
    stop();
    expect(cleanup).toHaveBeenCalledOnce();
  });
});
