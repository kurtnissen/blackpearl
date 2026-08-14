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
    .mockResolvedValueOnce(new Response(JSON.stringify({ configured: false }), { status: 200 }))
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
  expect(fetchSpy).toHaveBeenNthCalledWith(3, "/api/acquisition/settings", expect.objectContaining({
    body: JSON.stringify({ baseUrl: "http://prowlarr:9696", apiKey: "private-prowlarr-key" }),
  }));
  expect(fetchSpy).toHaveBeenNthCalledWith(4, "/api/acquisition/acquire", expect.objectContaining({
    body: JSON.stringify({ mediaType: "movie", title: "New Movie", year: 2026 }),
  }));
});

function requireVisibleCheckboxCount(count: number): void {
	expect(screen.getAllByRole("checkbox")).toHaveLength(count);
}
