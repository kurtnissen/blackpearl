import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { SetupConsole } from "./setup-console";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

it("discovers videos, applies a multi-item manifest, and clears the token field", async () => {
  const session = "a".repeat(64);
  const bootstrap = "b".repeat(64);
  window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
  const fetchSpy = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ candidates: [
			{ objectId: "17:3", name: "Films/Example.mkv", extension: ".mkv", size: 1073741824 },
			{ objectId: "17:4", name: "Films/Second.mp4", extension: ".mp4", size: 734003200 },
		] }), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }))
		.mockResolvedValueOnce(new Response(JSON.stringify({
			selected: { objectId: "17:3", name: "Films/Example.mkv", extension: ".mkv", size: 1073741824, mediaType: "movie", title: "Example", year: 2026 },
			selectedItems: [
				{ objectId: "17:3", name: "Films/Example.mkv", extension: ".mkv", size: 1073741824, mediaType: "movie", title: "Example", year: 2026 },
				{ objectId: "17:4", name: "Films/Second.mp4", extension: ".mp4", size: 734003200, mediaType: "movie", title: "Second", year: 2026 },
			],
		}), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }));
  vi.stubGlobal("fetch", fetchSpy);
  const user = userEvent.setup();
  render(<SetupConsole />);

  const token = await screen.findByLabelText("TorBox API token");
  await user.type(token, "private-token");
  await user.click(screen.getByRole("button", { name: "Find my videos" }));
	await user.click(await screen.findByRole("checkbox", { name: /Example\.mkv/ }));
	await user.click(screen.getByRole("checkbox", { name: /Second\.mp4/ }));
	await user.click(screen.getByRole("button", { name: "Use 2 with Plex" }));

  expect(await screen.findByText("BlackPearl is ready" )).toBeInTheDocument();
  expect(window.location.hash).toBe("");
  expect(window.sessionStorage.getItem("blackpearl.setup.session")).toBe(session);
  expect(Object.values(window.sessionStorage)).not.toContain("private-token");
  expect(fetchSpy).toHaveBeenNthCalledWith(2, "/api/setup/discover", expect.objectContaining({
    headers: expect.objectContaining({ "X-BlackPearl-Bootstrap": bootstrap }),
  }));
	expect(fetchSpy).toHaveBeenNthCalledWith(3, "/api/setup/configuration", expect.objectContaining({
		body: expect.stringContaining('"items"'),
	}));
  await user.click(screen.getByRole("button", { name: "Replace token" }));
  expect(screen.getByLabelText("TorBox API token")).toHaveValue("");
});

it("shows a helpful empty state when no eligible videos exist", async () => {
  const session = "a".repeat(64);
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ candidates: [] }), { status: 200, headers: { "X-BlackPearl-Session": session } })));
  const user = userEvent.setup();
  render(<SetupConsole />);

  await user.type(await screen.findByLabelText("TorBox API token"), "private-token");
  await user.click(screen.getByRole("button", { name: "Find my videos" }));

  expect(await screen.findByText("No ready MP4 or MKV files found")).toBeInTheDocument();
});

it("bounds the visible account list and searches the full discovery result", async () => {
	const session = "a".repeat(64);
	const candidates = Array.from({ length: 125 }, (_, index) => ({
		objectId: `17:${index + 1}`,
		name: index === 124 ? "Films/Needle.mp4" : `Films/Video-${index + 1}.mp4`,
		extension: ".mp4",
		size: 1024 + index,
	}));
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ candidates }), { status: 200, headers: { "X-BlackPearl-Session": session } })));
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.type(await screen.findByLabelText("TorBox API token"), "private-token");
	await user.click(screen.getByRole("button", { name: "Find my videos" }));
	requireVisibleCheckboxCount(100);
	await user.type(screen.getByLabelText("Search videos"), "Needle");

	expect(screen.getAllByRole("checkbox")).toHaveLength(1);
	expect(screen.getByRole("checkbox", { name: /Needle\.mp4/ })).toBeInTheDocument();
	expect(screen.queryByText(/Showing the first/)).not.toBeInTheDocument();
});

it("keeps the active manifest selected when adding videos with the saved token", async () => {
	const session = "a".repeat(64);
	const bootstrap = "b".repeat(64);
	const active = { objectId: "17:3", name: "Films/Existing.mp4", extension: ".mp4", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse())
		.mockResolvedValueOnce(new Response(JSON.stringify({ candidates: [
			{ objectId: "17:3", name: "Films/Existing.mp4", extension: ".mp4", size: 1024 },
			{ objectId: "17:4", name: "Films/New.mp4", extension: ".mp4", size: 2048 },
		] }), { status: 200, headers: { "X-BlackPearl-Session": session } })));
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.click(await screen.findByRole("button", { name: "Change video" }));

	expect(await screen.findByRole("checkbox", { name: /Existing\.mp4/ })).toBeChecked();
	expect(screen.getByLabelText("Plex title")).toHaveValue("Existing");
});

it("suggests explicit TV metadata for an SxxEyy filename", async () => {
	const session = "a".repeat(64);
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ candidates: [
			{
				objectId: "17:9",
				name: "Friends.1994.Season.7.Complete/Friends - S07E01 - The One With Monica's Thunder [4K h265].mkv",
				extension: ".mkv",
				size: 2048,
			},
		] }), { status: 200, headers: { "X-BlackPearl-Session": session } })));
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.type(await screen.findByLabelText("TorBox API token"), "private-token");
	await user.click(screen.getByRole("button", { name: "Find my videos" }));
	await user.click(await screen.findByRole("checkbox", { name: /Friends - S07E01/ }));

	expect(screen.getByLabelText("Media type")).toHaveValue("episode");
	expect(screen.getByLabelText("Show title")).toHaveValue("Friends");
	expect(screen.getByLabelText("Year")).toHaveValue(1994);
	expect(screen.getByLabelText("Season")).toHaveValue(7);
	expect(screen.getByLabelText("Episode")).toHaveValue(1);
	expect(screen.getByLabelText("Episode title")).toHaveValue("The One With Monica's Thunder");
});

it("announces provider errors without displaying the typed token", async () => {
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ code: "unauthorized", message: "That TorBox API key is invalid or expired. Open TorBox Settings, select Copy API Key, and try again." }), { status: 401 })));
  const user = userEvent.setup();
  render(<SetupConsole />);

  await user.type(await screen.findByLabelText("TorBox API token"), "private-token");
  await user.click(screen.getByRole("button", { name: "Find my videos" }));

  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("API key is invalid or expired"));
  expect(screen.queryByText("private-token")).not.toBeInTheDocument();
});

it("directs users to the TorBox API key instead of account credentials", async () => {
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 })));
  render(<SetupConsole />);

  expect(await screen.findByText(/not your password or Auth ID/i)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Copy your TorBox API key" })).toHaveAttribute("href", "https://torbox.app/settings");
});

it("lets users reveal and re-mask the TorBox key before submitting", async () => {
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 })));
  const user = userEvent.setup();
  render(<SetupConsole />);

  const token = await screen.findByLabelText("TorBox API token");
  await user.type(token, "example-key");
  expect(token).toHaveAttribute("type", "password");
  expect(screen.getByText("11 characters")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Show key" }));
  expect(token).toHaveAttribute("type", "text");
  expect(screen.getByRole("button", { name: "Hide key" })).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Hide key" }));
  expect(token).toHaveAttribute("type", "password");
});

it("limits the Plex title to the API filename bound", async () => {
  const session = "a".repeat(64);
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ candidates: [{ objectId: "17:3", name: "Films/Example.mkv", extension: ".mkv", size: 10 }] }), { status: 200, headers: { "X-BlackPearl-Session": session } })));
  const user = userEvent.setup();
  render(<SetupConsole />);

  await user.type(await screen.findByLabelText("TorBox API token"), "private-token");
  await user.click(screen.getByRole("button", { name: "Find my videos" }));
	await user.click(await screen.findByRole("checkbox", { name: /Example\.mkv/ }));

  expect(screen.getByLabelText("Plex title")).toHaveAttribute("maxLength", "200");
});

it("connects Prowlarr and adds an instant cached movie to the Plex manifest", async () => {
  const session = "a".repeat(64);
  const bootstrap = "b".repeat(64);
  const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
  const added = { objectId: "18:2", name: "New.Movie.2026.mkv", extension: ".mkv", size: 2048, mediaType: "movie", title: "New Movie", year: 2026 };
  window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
  const fetchSpy = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
    .mockResolvedValueOnce(watchlistResponse())
    .mockResolvedValueOnce(new Response(JSON.stringify({ configured: false }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ jobs: [] }), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ configured: true }), { status: 200, headers: { "X-BlackPearl-Session": session } }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ selected: added, selectedItems: [active, added] }), { status: 200, headers: { "X-BlackPearl-Session": session } }));
  vi.stubGlobal("fetch", fetchSpy);
  const user = userEvent.setup();
  render(<SetupConsole />);

  await user.click(await screen.findByRole("button", { name: "Find something new" }));
  expect(await screen.findByLabelText("Prowlarr URL")).toHaveValue("http://prowlarr:9696");
  await user.type(screen.getByLabelText("Prowlarr API key"), "private-prowlarr-key");
  await user.click(screen.getByRole("button", { name: "Connect Prowlarr" }));

  await user.type(await screen.findByLabelText("Title"), "New Movie");
  await user.clear(screen.getByLabelText("Year"));
  await user.type(screen.getByLabelText("Year"), "2026");
  await user.click(screen.getByRole("button", { name: "Find and add to Plex" }));

  expect(await screen.findByText(/New Movie \(2026\)/)).toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Added New Movie to Plex");
  expect(window.sessionStorage.getItem("blackpearl.setup.session")).toBe(session);
  expect(Object.values(window.sessionStorage)).not.toContain("private-prowlarr-key");
  expect(fetchSpy).toHaveBeenNthCalledWith(5, "/api/acquisition/settings", expect.objectContaining({
    body: JSON.stringify({ baseUrl: "http://prowlarr:9696", apiKey: "private-prowlarr-key" }),
  }));
  expect(fetchSpy).toHaveBeenNthCalledWith(6, "/api/acquisition/acquire", expect.objectContaining({
    body: JSON.stringify({ mediaType: "movie", title: "New Movie", year: 2026 }),
  }));
});

it("offers explicit TorBox preparation when no instant copy is cached", async () => {
	const session = "a".repeat(64);
	const bootstrap = "b".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	const job = {
		id: "0123456789abcdef0123456789abcdef",
		state: "queued",
		mediaType: "movie",
		title: "Open Movie",
		year: 2026,
		progress: 0,
		createdAt: "2026-08-14T12:00:00Z",
		updatedAt: "2026-08-14T12:00:00Z",
	};
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	const fetchSpy = vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse())
		.mockResolvedValueOnce(new Response(JSON.stringify({ configured: true }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ jobs: [] }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ code: "not_cached", message: "No instant result." }), { status: 404 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ job, created: true }), {
			status: 202,
			headers: { "X-BlackPearl-Session": session },
		}));
	vi.stubGlobal("fetch", fetchSpy);
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.click(await screen.findByRole("button", { name: "Find something new" }));
	await user.type(await screen.findByLabelText("Title"), "Open Movie");
	await user.clear(screen.getByLabelText("Year"));
	await user.type(screen.getByLabelText("Year"), "2026");
	await user.click(screen.getByRole("button", { name: "Find and add to Plex" }));
	await user.click(await screen.findByRole("button", { name: "Prepare through TorBox" }));

	expect(await screen.findByRole("heading", { name: "Open Movie" })).toBeInTheDocument();
	expect(screen.getByText(/continues if you close this page/i)).toBeInTheDocument();
	expect(screen.getByText("Queued")).toBeInTheDocument();
	expect(fetchSpy).toHaveBeenNthCalledWith(6, "/api/acquisition/jobs", expect.objectContaining({
		method: "POST",
		body: JSON.stringify({ mediaType: "movie", title: "Open Movie", year: 2026 }),
	}));
});

it("explains when TorBox preparation stalls without a source", async () => {
	const bootstrap = "b".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	const stalled = {
		id: "0123456789abcdef0123456789abcdef",
		state: "failed",
		mediaType: "movie",
		title: "Open Movie",
		year: 2026,
		progress: 0,
		errorCode: "stalled",
		createdAt: "2026-08-14T12:00:00Z",
		updatedAt: "2026-08-14T12:05:00Z",
	};
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse())
		.mockResolvedValueOnce(new Response(JSON.stringify({ configured: true }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ jobs: [stalled] }), { status: 200 })));
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.click(await screen.findByRole("button", { name: "Find something new" }));

	expect(await screen.findByText(/no source was available/i)).toBeInTheDocument();
	expect(screen.getByText(/Search again to try another verified release/i)).toBeInTheDocument();
});

it("shows the exact durable provider progress while media is preparing", async () => {
	const bootstrap = "b".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	const preparing = {
		id: "0123456789abcdef0123456789abcdef",
		state: "preparing",
		mediaType: "movie",
		title: "Open Movie",
		year: 2026,
		progress: 12,
		createdAt: "2026-08-14T12:00:00Z",
		updatedAt: "2026-08-14T12:05:00Z",
	};
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse())
		.mockResolvedValueOnce(new Response(JSON.stringify({ configured: true }), { status: 200 }))
		.mockResolvedValueOnce(new Response(JSON.stringify({ jobs: [preparing] }), { status: 200 })));
	const user = userEvent.setup();
	render(<SetupConsole />);
	await user.click(await screen.findByRole("button", { name: "Find something new" }));

	const progress = await screen.findByRole("progressbar", { name: "Open Movie preparation progress" });

	expect(progress).toHaveAttribute("value", "12");
	expect(screen.getByText("12% prepared")).toBeInTheDocument();
});

it("shows aggregate Plex Watchlist activity without exposing titles or identifiers", async () => {
  const bootstrap = "b".repeat(64);
  const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
  window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
    .mockResolvedValueOnce(watchlistResponse()));

  render(<SetupConsole />);

  expect(await screen.findByRole("heading", { name: "Plex Watchlist" })).toBeInTheDocument();
  expect(screen.getByText("OBSERVING")).toBeInTheDocument();
  expect(screen.getByText("3 movies observed")).toBeInTheDocument();
  expect(screen.getByText("2 shows observed")).toBeInTheDocument();
  expect(screen.getByText(/Automatic adding stays off/)).toBeInTheDocument();
  expect(screen.queryByText(/objectId/i)).not.toBeInTheDocument();
});

it("explains that automatic intake applies only to newly observed authorized movies", async () => {
  const bootstrap = "b".repeat(64);
  const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
  window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
    .mockResolvedValueOnce(watchlistResponse(true)));

  render(<SetupConsole />);

  expect(await screen.findByText("AUTO ADD ON")).toBeInTheDocument();
  expect(screen.getByText(/new authorized movies added after auto add was enabled/i)).toBeInTheDocument();
  expect(screen.getByText(/TorBox may download an uncached release/i)).toBeInTheDocument();
});

it("turns automatic Watchlist intake on without commands", async () => {
	const bootstrap = "b".repeat(64);
	const session = "a".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	const fetchSpy = vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse())
		.mockResolvedValueOnce(new Response(await watchlistResponse(true).text(), {
			status: 200,
			headers: { "X-BlackPearl-Session": session },
		}));
	vi.stubGlobal("fetch", fetchSpy);
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.click(await screen.findByRole("button", { name: "Turn automatic adding on" }));

	expect(await screen.findByText("AUTO ADD ON")).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Turn automatic adding off" })).toBeInTheDocument();
	expect(fetchSpy).toHaveBeenLastCalledWith("/api/watchlist/settings", expect.objectContaining({
		method: "PUT",
		body: JSON.stringify({ acquisitionEnabled: true, showPolicy: "off" }),
	}));
});

it("can start only S01E01 for shows newly added after pilot intake is enabled", async () => {
	const bootstrap = "b".repeat(64);
	const session = "a".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	const fetchSpy = vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse(true))
		.mockResolvedValueOnce(new Response(await watchlistResponse(true, "pilot").text(), {
			status: 200,
			headers: { "X-BlackPearl-Session": session },
		}));
	vi.stubGlobal("fetch", fetchSpy);
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.click(await screen.findByRole("button", { name: "Start new shows with S01E01" }));

	expect(await screen.findByRole("button", { name: "Stop starting new shows" })).toBeInTheDocument();
	expect(screen.getByText(/never adds a full season/i)).toBeInTheDocument();
	expect(fetchSpy).toHaveBeenLastCalledWith("/api/watchlist/settings", expect.objectContaining({
		method: "PUT",
		body: JSON.stringify({ acquisitionEnabled: true, showPolicy: "pilot" }),
	}));
});

it("keeps show pilot intake unavailable while automatic Watchlist adding is off", async () => {
	const bootstrap = "b".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse(false)));
	render(<SetupConsole />);

	expect(await screen.findByRole("button", { name: "Start new shows with S01E01" })).toBeDisabled();
});

it("keeps show pilot intake off when saving the setting fails", async () => {
	const bootstrap = "b".repeat(64);
	const active = { objectId: "17:3", name: "Existing.mkv", extension: ".mkv", size: 1024, mediaType: "movie", title: "Existing", year: 2024 };
	window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
	vi.stubGlobal("fetch", vi.fn()
		.mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: false, tokenConfigured: true, csrfToken: "csrf", selected: active, selectedItems: [active] }), { status: 200 }))
		.mockResolvedValueOnce(watchlistResponse(true))
		.mockResolvedValueOnce(new Response(JSON.stringify({
			code: "watchlist_unavailable",
			message: "The Watchlist setting could not be saved right now.",
		}), { status: 503 })));
	const user = userEvent.setup();
	render(<SetupConsole />);

	await user.click(await screen.findByRole("button", { name: "Start new shows with S01E01" }));

	expect(await screen.findByText(/Watchlist status is temporarily unavailable/i)).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Start new shows with S01E01" })).toHaveAttribute("aria-pressed", "false");
});

function watchlistResponse(acquisitionEnabled = false, showPolicy: "off" | "pilot" = "off"): Response {
  return new Response(JSON.stringify({
    enabled: true,
    healthy: true,
    acquisitionEnabled,
		showPolicy,
    lastSyncAt: "2026-08-14T14:00:00Z",
    queue: {
      pendingMovies: 3,
      acquiring: 0,
      succeeded: 1,
      notCached: 0,
      retryable: 0,
      manualReview: 0,
      observedShows: 2,
    },
  }), { status: 200 });
}

function requireVisibleCheckboxCount(count: number): void {
	expect(screen.getAllByRole("checkbox")).toHaveLength(count);
}
