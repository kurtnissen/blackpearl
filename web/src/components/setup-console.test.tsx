import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { SetupConsole } from "./setup-console";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

it("discovers a video, applies it, and clears the token field", async () => {
  const session = "a".repeat(64);
  const bootstrap = "b".repeat(64);
  window.history.replaceState(null, "", `/#bootstrap=${bootstrap}`);
  const fetchSpy = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ candidates: [{ objectId: "17:3", name: "Films/Example.mkv", extension: ".mkv", size: 1073741824 }] }), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ selected: { objectId: "17:3", name: "Films/Example.mkv", extension: ".mkv", size: 1073741824, title: "Example", year: 2026 } }), {
      status: 200,
      headers: { "X-BlackPearl-Session": session },
    }));
  vi.stubGlobal("fetch", fetchSpy);
  const user = userEvent.setup();
  render(<SetupConsole />);

  const token = await screen.findByLabelText("TorBox API token");
  await user.type(token, "private-token");
  await user.click(screen.getByRole("button", { name: "Find my videos" }));
  await user.click(await screen.findByRole("radio", { name: /Example\.mkv/ }));
  await user.click(screen.getByRole("button", { name: "Use with Plex" }));

  expect(await screen.findByText("BlackPearl is ready" )).toBeInTheDocument();
  expect(window.location.hash).toBe("");
  expect(window.sessionStorage.getItem("blackpearl.setup.session")).toBe(session);
  expect(Object.values(window.sessionStorage)).not.toContain("private-token");
  expect(fetchSpy).toHaveBeenNthCalledWith(2, "/api/setup/discover", expect.objectContaining({
    headers: expect.objectContaining({ "X-BlackPearl-Bootstrap": bootstrap }),
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

it("announces provider errors without displaying the typed token", async () => {
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ setupRequired: true, tokenConfigured: false, csrfToken: "csrf" }), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ code: "unauthorized", message: "TorBox did not accept those credentials." }), { status: 401 })));
  const user = userEvent.setup();
  render(<SetupConsole />);

  await user.type(await screen.findByLabelText("TorBox API token"), "private-token");
  await user.click(screen.getByRole("button", { name: "Find my videos" }));

  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("TorBox did not accept"));
  expect(screen.queryByText("private-token")).not.toBeInTheDocument();
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
  await user.click(await screen.findByRole("radio", { name: /Example\.mkv/ }));

  expect(screen.getByLabelText("Plex title")).toHaveAttribute("maxLength", "200");
});
