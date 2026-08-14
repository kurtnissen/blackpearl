"use client";

import { useEffect, useMemo, useState } from "react";
import {
  applyConfiguration,
  discoverMedia,
  getStatus,
  SetupAPIError,
  type MediaCandidate,
  type SetupAuthorization,
  type SetupConfiguration,
} from "../lib/api";

type Phase = "loading" | "credentials" | "select" | "ready";
const setupSessionStorageKey = "blackpearl.setup.session";
const setupBootstrapStorageKey = "blackpearl.setup.bootstrap";

export function SetupConsole(): React.JSX.Element {
  const [phase, setPhase] = useState<Phase>("loading");
  const [csrf, setCSRF] = useState("");
  const [tokenConfigured, setTokenConfigured] = useState(false);
  const [token, setToken] = useState("");
  const [session, setSession] = useState("");
  const [bootstrap, setBootstrap] = useState("");
  const [candidates, setCandidates] = useState<MediaCandidate[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [title, setTitle] = useState("");
  const [year, setYear] = useState(new Date().getFullYear());
  const [selected, setSelected] = useState<SetupConfiguration>();
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("Loading BlackPearl setup…");

  const selectedCandidate = useMemo(
    () => candidates.find((candidate) => candidate.objectId === selectedID),
    [candidates, selectedID],
  );
  const authorization: SetupAuthorization = { session, bootstrap };
  const canUseSavedToken = tokenConfigured && (session !== "" || bootstrap !== "");

  useEffect(() => {
    let active = true;
    const storedAuthorization = loadBrowserAuthorization();
    getStatus()
      .then((status) => {
        if (!active) return;
        setSession(storedAuthorization.session ?? "");
        setBootstrap(storedAuthorization.bootstrap ?? "");
        setCSRF(status.csrfToken);
        setTokenConfigured(status.tokenConfigured);
        if (!status.setupRequired && status.selected) {
          setSelected(status.selected);
          setPhase("ready");
          setMessage("BlackPearl is ready for Plex.");
        } else {
          setPhase("credentials");
          setMessage(status.tokenConfigured && !storedAuthorization.session && !storedAuthorization.bootstrap
            ? "Re-enter your saved TorBox token to authorize this browser."
            : "Enter your TorBox token to find ready videos.");
        }
      })
      .catch((error: unknown) => {
        if (!active) return;
        setSession(storedAuthorization.session ?? "");
        setBootstrap(storedAuthorization.bootstrap ?? "");
        setPhase("credentials");
        setMessage(publicMessage(error));
      });
    return () => { active = false; };
  }, []);

  async function findVideos(useSavedToken = false): Promise<void> {
    setPending(true);
    setMessage("Reading your completed TorBox files…");
    try {
      const result = await discoverMedia(useSavedToken ? "" : token, csrf, authorization);
      storeSession(result.session);
      setSession(result.session);
      setCandidates(result.candidates);
      setSelectedID("");
      if (result.candidates.length === 0) {
        setMessage("No ready MP4 or MKV files found");
        return;
      }
      setPhase("select");
      setMessage(`${result.candidates.length} ready ${result.candidates.length === 1 ? "video" : "videos"} found.`);
    } catch (error: unknown) {
      setMessage(publicMessage(error));
    } finally {
      setPending(false);
    }
  }

  function choose(candidate: MediaCandidate): void {
    setSelectedID(candidate.objectId);
    setTitle(suggestTitle(candidate.name));
    setYear(new Date().getFullYear());
  }

  async function apply(): Promise<void> {
    if (!selectedCandidate) return;
    setPending(true);
    setMessage("Preparing the rolling stream for Plex…");
    try {
      const result = await applyConfiguration({
        token: token === "" ? undefined : token,
        objectId: selectedCandidate.objectId,
        title,
        year,
      }, csrf, authorization);
      storeSession(result.session);
      setSession(result.session);
      setToken("");
      setTokenConfigured(true);
      setSelected(result.selected);
      setPhase("ready");
      setMessage("BlackPearl is ready for Plex.");
    } catch (error: unknown) {
      setMessage(publicMessage(error));
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="shell">
      <header className="masthead">
        <div className="brand-mark" aria-hidden="true">BP</div>
        <div>
          <p className="eyebrow">LOCAL STREAM CONTROL</p>
          <h1>BlackPearl</h1>
        </div>
        <div className={`signal signal--${phase}`}><span />{phaseLabel(phase)}</div>
      </header>

      <section className="manifest" aria-labelledby="manifest-title">
        <div className="manifest__heading">
          <div>
            <p className="folio">TORBOX / PLEX MANIFEST</p>
            <h2 id="manifest-title">{phase === "ready" ? "Stream assigned" : "Choose what Plex sees"}</h2>
          </div>
          <p className="route">RANGE → CACHE → NFS → PLEX</p>
        </div>

        <p className="status-line" role="status" aria-live="polite">{message}</p>

        {phase === "loading" && <div className="loading-rule" aria-hidden="true" />}

        {phase === "credentials" && (
          <form className="credential-form" onSubmit={(event) => { event.preventDefault(); void findVideos(false); }}>
            <label htmlFor="torbox-token">TorBox API token</label>
            <p className="field-note">
              Use the API key from TorBox Settings, not your password or Auth ID. <a href="https://torbox.app/settings" target="_blank" rel="noreferrer">Copy your TorBox API key</a>.
              It is stored only inside BlackPearl&apos;s private Docker volume and is never shown again.
            </p>
            <input
              id="torbox-token"
              name="torbox-token"
              type="password"
              autoComplete="new-password"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              required={!tokenConfigured}
              disabled={pending}
            />
            <div className="actions">
              <button className="primary" type="submit" disabled={pending || (!tokenConfigured && token.length === 0)}>Find my videos</button>
              {canUseSavedToken && <button type="button" onClick={() => void findVideos(true)} disabled={pending}>Use saved token</button>}
            </div>
          </form>
        )}

        {phase === "select" && (
          <div className="selection">
            <fieldset>
              <legend>Eligible account files</legend>
              <div className="candidate-list">
                {candidates.map((candidate) => (
                  <label className="candidate" key={candidate.objectId}>
                    <input type="radio" name="candidate" checked={selectedID === candidate.objectId} onChange={() => choose(candidate)} />
                    <span className="candidate__name">{candidate.name}</span>
                    <span className="candidate__meta">{candidate.extension.slice(1).toUpperCase()} · {formatBytes(candidate.size)}</span>
                  </label>
                ))}
              </div>
            </fieldset>
            {selectedCandidate && (
              <div className="plex-fields">
                <label>Plex title<input value={title} maxLength={200} onChange={(event) => setTitle(event.target.value)} /></label>
                <label>Year<input type="number" min="1888" max="2100" value={year} onChange={(event) => setYear(event.target.valueAsNumber)} /></label>
              </div>
            )}
            <div className="actions">
              <button className="primary" type="button" onClick={() => void apply()} disabled={pending || !selectedCandidate || title.trim() === ""}>Use with Plex</button>
              <button type="button" onClick={() => setPhase("credentials")} disabled={pending}>Back</button>
            </div>
          </div>
        )}

        {phase === "ready" && selected && (
          <div className="ready-card">
            <p className="ready-kicker">ASSIGNED MEDIA</p>
            <h3>BlackPearl is ready</h3>
            <dl>
              <div><dt>Plex title</dt><dd>{selected.title} ({selected.year})</dd></div>
              <div><dt>Source file</dt><dd>{selected.name}</dd></div>
              <div><dt>Logical size</dt><dd>{formatBytes(selected.size)}</dd></div>
              <div><dt>Storage</dt><dd>Rolling range cache</dd></div>
            </dl>
            <div className="actions">
              <a className="primary button-link" href="http://localhost:32402/web" target="_blank" rel="noreferrer">Open Plex</a>
              {canUseSavedToken
                ? <button type="button" onClick={() => void findVideos(true)} disabled={pending}>Change video</button>
                : <button type="button" onClick={() => { setToken(""); setPhase("credentials"); setMessage("Re-enter your saved TorBox token to authorize this browser."); }}>Change video</button>}
              <button type="button" onClick={() => { setToken(""); setPhase("credentials"); setMessage("Enter a replacement TorBox token."); }}>Replace token</button>
            </div>
          </div>
        )}
      </section>

      <footer><span>LOCALHOST ONLY</span><span>NO COMPLETE FILE REQUIRED</span><span>DIRECT PLAY TARGET</span></footer>
    </main>
  );
}

function loadBrowserAuthorization(): SetupAuthorization {
  const session = validSetupSecret(window.sessionStorage.getItem(setupSessionStorageKey));
  let bootstrap = validSetupSecret(window.sessionStorage.getItem(setupBootstrapStorageKey));
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  const fragmentBootstrap = validSetupSecret(fragment.get("bootstrap"));
  if (fragmentBootstrap) {
    bootstrap = fragmentBootstrap;
    window.sessionStorage.setItem(setupBootstrapStorageKey, fragmentBootstrap);
  }
  if (window.location.hash !== "") {
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}`);
  }
  return { session, bootstrap };
}

function storeSession(session: string): void {
  window.sessionStorage.setItem(setupSessionStorageKey, session);
}

function validSetupSecret(value: string | null): string | undefined {
  return value !== null && /^[0-9a-f]{64}$/.test(value) ? value : undefined;
}

function publicMessage(error: unknown): string {
  if (error instanceof SetupAPIError) return error.message;
  return "BlackPearl could not reach its local setup service.";
}

function suggestTitle(name: string): string {
  const base = name.split("/").at(-1) ?? name;
  return base.replace(/\.(mkv|mp4)$/i, "").replace(/[._]+/g, " ").replace(/\s+/g, " ").trim();
}

function phaseLabel(phase: Phase): string {
  if (phase === "ready") return "READY";
  if (phase === "loading") return "STARTING";
  return "SETUP";
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = -1;
  do {
    value /= 1024;
    unit += 1;
  } while (value >= 1024 && unit < units.length - 1);
  return `${value.toFixed(value >= 10 ? 1 : 2)} ${units[unit]}`;
}
