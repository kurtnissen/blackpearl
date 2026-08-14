import { afterEach, describe, expect, it, vi } from "vitest";
import { applyConfiguration, discoverMedia, getStatus } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("setup API", () => {
  it("uses same-origin no-store requests and forwards CSRF", async () => {
    const fetchSpy = vi.fn(async () => new Response(JSON.stringify({ candidates: [] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchSpy);

    await discoverMedia("private-token", "csrf-value");

    expect(fetchSpy).toHaveBeenCalledWith("/api/setup/discover", {
      method: "POST",
      cache: "no-store",
      headers: { "Content-Type": "application/json", "X-BlackPearl-CSRF": "csrf-value" },
      body: JSON.stringify({ token: "private-token" }),
    });
  });

  it("maps public API errors without exposing response internals", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "unauthorized", message: "Token rejected" }), { status: 401 })));

    await expect(discoverMedia("bad-token", "csrf")).rejects.toMatchObject({ code: "unauthorized", message: "Token rejected" });
  });

  it("loads status and applies the selected public metadata", async () => {
    const fetchSpy = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ selected: { objectId: "17:3", name: "Film.mkv", extension: ".mkv", size: 9, title: "Film", year: 2026 } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchSpy);

    const status = await getStatus();
    const selected = await applyConfiguration({ objectId: "17:3", title: "Film", year: 2026 }, status.csrfToken);

    expect(selected.extension).toBe(".mkv");
    expect(fetchSpy).toHaveBeenLastCalledWith("/api/setup/configuration", expect.objectContaining({ method: "PUT", cache: "no-store" }));
  });
});
