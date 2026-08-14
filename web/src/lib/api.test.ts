import { afterEach, describe, expect, it, vi } from "vitest";
import {
  acquireMedia,
  applyConfiguration,
  configureAcquisition,
  discoverMedia,
  getAcquisitionStatus,
  getStatus,
} from "./api";

const session = "a".repeat(64);
const bootstrap = "b".repeat(64);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("setup API", () => {
  it("uses same-origin no-store requests and forwards CSRF", async () => {
    const fetchSpy = vi.fn(async () => new Response(JSON.stringify({ candidates: [] }), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }));
    vi.stubGlobal("fetch", fetchSpy);

    const result = await discoverMedia("private-token", "csrf-value", { session, bootstrap });

    expect(fetchSpy).toHaveBeenCalledWith("/api/setup/discover", {
      method: "POST",
      cache: "no-store",
      headers: {
        "Content-Type": "application/json",
        "X-BlackPearl-CSRF": "csrf-value",
        "X-BlackPearl-Session": session,
        "X-BlackPearl-Bootstrap": bootstrap,
      },
      body: JSON.stringify({ token: "private-token" }),
    });
    expect(result).toEqual({ candidates: [], session });
  });

  it("maps public API errors without exposing response internals", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "unauthorized", message: "Token rejected" }), { status: 401 })));

    await expect(discoverMedia("bad-token", "csrf", {})).rejects.toMatchObject({ code: "unauthorized", message: "Token rejected" });
  });

  it("loads status and applies a public media manifest", async () => {
	const selectedItems = [{ objectId: "17:3", name: "Film.mkv", extension: ".mkv", size: 9, mediaType: "movie", title: "Film", year: 2026 }];
    const fetchSpy = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ selected: selectedItems[0], selectedItems }), {
        status: 200,
        headers: { "X-BlackPearl-Session": session },
      }));
    vi.stubGlobal("fetch", fetchSpy);

    const status = await getStatus();
	const result = await applyConfiguration({ items: [{ objectId: "17:3", mediaType: "movie", title: "Film", year: 2026 }] }, status.csrfToken, { session });

    expect(result.selected.extension).toBe(".mkv");
	expect(result.selectedItems).toHaveLength(1);
    expect(result.session).toBe(session);
    expect(fetchSpy).toHaveBeenLastCalledWith("/api/setup/configuration", expect.objectContaining({ method: "PUT", cache: "no-store" }));
  });

  it("rejects a successful mutation response without a valid setup session", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ candidates: [] }), { status: 200 })));

    await expect(discoverMedia("private-token", "csrf", { bootstrap })).rejects.toMatchObject({ code: "invalid_session" });
  });

  it("loads only the public acquisition configuration state", async () => {
    const fetchSpy = vi.fn(async () => new Response(JSON.stringify({ configured: true }), { status: 200 }));
    vi.stubGlobal("fetch", fetchSpy);

    await expect(getAcquisitionStatus()).resolves.toEqual({ configured: true });
    expect(fetchSpy).toHaveBeenCalledWith("/api/acquisition/status", { method: "GET", cache: "no-store" });
  });

  it("configures Prowlarr through the paired mutation boundary", async () => {
    const fetchSpy = vi.fn(async () => new Response(JSON.stringify({ configured: true }), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }));
    vi.stubGlobal("fetch", fetchSpy);

    const result = await configureAcquisition(
      { baseUrl: "http://prowlarr:9696", apiKey: "private-key" },
      "csrf-value",
      { session, bootstrap },
    );

    expect(result).toEqual({ configured: true, session });
    expect(fetchSpy).toHaveBeenCalledWith("/api/acquisition/settings", {
      method: "PUT",
      cache: "no-store",
      headers: {
        "Content-Type": "application/json",
        "X-BlackPearl-CSRF": "csrf-value",
        "X-BlackPearl-Session": session,
        "X-BlackPearl-Bootstrap": bootstrap,
      },
      body: JSON.stringify({ baseUrl: "http://prowlarr:9696", apiKey: "private-key" }),
    });
  });

  it("acquires a cached movie and validates the returned Plex manifest", async () => {
    const selectedItems = [{ objectId: "18:2", name: "Film.mkv", extension: ".mkv", size: 9, mediaType: "movie", title: "Film", year: 2026 }];
    const fetchSpy = vi.fn(async () => new Response(JSON.stringify({ selected: selectedItems[0], selectedItems }), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }));
    vi.stubGlobal("fetch", fetchSpy);

    const result = await acquireMedia(
      { mediaType: "movie", title: "Film", year: 2026 },
      "csrf-value",
      { session },
    );

    expect(result.selectedItems).toHaveLength(1);
    expect(result.session).toBe(session);
    expect(fetchSpy).toHaveBeenCalledWith("/api/acquisition/acquire", expect.objectContaining({
      method: "POST",
      cache: "no-store",
      body: JSON.stringify({ mediaType: "movie", title: "Film", year: 2026 }),
    }));
  });
});
