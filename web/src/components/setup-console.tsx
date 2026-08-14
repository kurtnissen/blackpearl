"use client";

import { useEffect, useMemo, useState } from "react";
import {
  applyConfiguration,
  discoverMedia,
  getStatus,
  SetupAPIError,
  type MediaCandidate,
	type ApplyItemInput,
  type SetupAuthorization,
  type SetupConfiguration,
} from "../lib/api";

type Phase = "loading" | "credentials" | "select" | "ready";
type SelectionDraft = {
	objectId: string;
	mediaType: "movie" | "episode";
	title: string;
	year: number;
	showTitle: string;
	season: number;
	episode: number;
};
const setupSessionStorageKey = "blackpearl.setup.session";
const setupBootstrapStorageKey = "blackpearl.setup.bootstrap";
const maximumManifestItems = 100;
const maximumVisibleCandidates = 100;

export function SetupConsole(): React.JSX.Element {
  const [phase, setPhase] = useState<Phase>("loading");
  const [csrf, setCSRF] = useState("");
  const [tokenConfigured, setTokenConfigured] = useState(false);
  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [session, setSession] = useState("");
  const [bootstrap, setBootstrap] = useState("");
  const [candidates, setCandidates] = useState<MediaCandidate[]>([]);
	const [query, setQuery] = useState("");
	const [drafts, setDrafts] = useState<SelectionDraft[]>([]);
	const [selectedItems, setSelectedItems] = useState<SetupConfiguration[]>([]);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("Loading BlackPearl setup…");

	const matchingCandidates = useMemo(() => {
		const normalized = query.trim().toLocaleLowerCase();
		return candidates
			.filter((candidate) => normalized === "" || candidate.name.toLocaleLowerCase().includes(normalized));
	}, [candidates, query]);
	const visibleCandidates = useMemo(
		() => matchingCandidates.slice(0, maximumVisibleCandidates),
		[matchingCandidates],
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
				const restoredItems = status.selectedItems ?? (status.selected ? [status.selected] : []);
				if (!status.setupRequired && restoredItems.length > 0) {
					setSelectedItems(restoredItems);
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
      setShowToken(false);
      setCandidates(result.candidates);
			setQuery("");
			const discoveredIDs = new Set(result.candidates.map((candidate) => candidate.objectId));
			setDrafts(useSavedToken
				? selectedItems
					.filter((item) => discoveredIDs.has(item.objectId))
					.map((item) => ({
						objectId: item.objectId, mediaType: item.mediaType, title: item.title, year: item.year,
						showTitle: item.showTitle ?? "", season: item.season ?? 1, episode: item.episode ?? 1,
					}))
				: []);
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

  function toggle(candidate: MediaCandidate): void {
		setDrafts((current) => {
			if (current.some((draft) => draft.objectId === candidate.objectId)) {
				return current.filter((draft) => draft.objectId !== candidate.objectId);
			}
			if (current.length >= maximumManifestItems) return current;
			return [...current, suggestDraft(candidate)];
		});
  }

	function updateDraft(objectId: string, update: Partial<Omit<SelectionDraft, "objectId">>): void {
		setDrafts((current) => current.map((draft) => draft.objectId === objectId ? { ...draft, ...update } : draft));
	}

	function replaceDraft(objectId: string, mediaType: "movie" | "episode"): void {
		const candidate = candidates.find((item) => item.objectId === objectId);
		if (!candidate) return;
		setDrafts((current) => current.map((draft) => draft.objectId === objectId ? suggestDraft(candidate, mediaType) : draft));
	}

  async function apply(): Promise<void> {
		if (drafts.length === 0) return;
    setPending(true);
    setMessage("Preparing the rolling stream for Plex…");
    try {
      const result = await applyConfiguration({
        token: token === "" ? undefined : token,
				items: drafts.map(toApplyItem),
      }, csrf, authorization);
      storeSession(result.session);
      setSession(result.session);
      setToken("");
      setTokenConfigured(true);
			setSelectedItems(result.selectedItems);
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
            <div className="token-input">
              <input
                id="torbox-token"
                name="torbox-token"
                type={showToken ? "text" : "password"}
                autoComplete="new-password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                required={!tokenConfigured}
                disabled={pending}
              />
              <button type="button" aria-controls="torbox-token" aria-pressed={showToken} onClick={() => setShowToken((visible) => !visible)} disabled={pending}>
                {showToken ? "Hide key" : "Show key"}
              </button>
            </div>
            <p className="token-meta" aria-live="polite">{token.length} {token.length === 1 ? "character" : "characters"}</p>
            <div className="actions">
              <button className="primary" type="submit" disabled={pending || (!tokenConfigured && token.length === 0)}>Find my videos</button>
              {canUseSavedToken && <button type="button" onClick={() => void findVideos(true)} disabled={pending}>Use saved token</button>}
            </div>
          </form>
        )}

        {phase === "select" && (
          <div className="selection">
            <fieldset>
						<div className="selection-tools">
							<div><legend>Eligible account files</legend><p>{drafts.length} of {maximumManifestItems} selected</p></div>
							<label className="search-field">Search videos<input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Title or filename" /></label>
						</div>
              <div className="candidate-list">
						{visibleCandidates.map((candidate) => (
                  <label className="candidate" key={candidate.objectId}>
								<input type="checkbox" checked={drafts.some((draft) => draft.objectId === candidate.objectId)} onChange={() => toggle(candidate)} />
                    <span className="candidate__name">{candidate.name}</span>
                    <span className="candidate__meta">{candidate.extension.slice(1).toUpperCase()} · {formatBytes(candidate.size)}</span>
                  </label>
                ))}
              </div>
						{matchingCandidates.length > visibleCandidates.length && <p className="result-note">Showing the first {visibleCandidates.length} matches. Search to narrow the list.</p>}
            </fieldset>
					{drafts.length > 0 && (
						<div className="selected-editor">
							<h3>Plex manifest</h3>
							{drafts.map((draft, index) => (
								<div className="plex-fields" key={draft.objectId}>
									<span className="selection-number">{String(index + 1).padStart(2, "0")}</span>
									<label>Media type<select value={draft.mediaType} onChange={(event) => replaceDraft(draft.objectId, mediaTypeFromValue(event.target.value))}><option value="movie">Movie</option><option value="episode">TV episode</option></select></label>
									{draft.mediaType === "episode" && <label>Show title<input value={draft.showTitle} maxLength={200} onChange={(event) => updateDraft(draft.objectId, { showTitle: event.target.value })} /></label>}
									<label>{draft.mediaType === "episode" ? "Episode title" : "Plex title"}<input value={draft.title} maxLength={200} onChange={(event) => updateDraft(draft.objectId, { title: event.target.value })} /></label>
									<label>Year<input type="number" min="1888" max="2100" value={draft.year} onChange={(event) => updateDraft(draft.objectId, { year: event.target.valueAsNumber })} /></label>
									{draft.mediaType === "episode" && <label>Season<input type="number" min="0" max="99" value={draft.season} onChange={(event) => updateDraft(draft.objectId, { season: event.target.valueAsNumber })} /></label>}
									{draft.mediaType === "episode" && <label>Episode<input type="number" min="1" max="999" value={draft.episode} onChange={(event) => updateDraft(draft.objectId, { episode: event.target.valueAsNumber })} /></label>}
									<button type="button" onClick={() => setDrafts((current) => current.filter((item) => item.objectId !== draft.objectId))}>Remove</button>
								</div>
							))}
						</div>
					)}
            <div className="actions">
						<button className="primary" type="button" onClick={() => void apply()} disabled={pending || drafts.length === 0 || drafts.some((draft) => !validDraft(draft))}>Use {drafts.length || "selected"} with Plex</button>
              <button type="button" onClick={() => setPhase("credentials")} disabled={pending}>Back</button>
            </div>
          </div>
        )}

				{phase === "ready" && selectedItems.length > 0 && (
          <div className="ready-card">
            <p className="ready-kicker">ASSIGNED MEDIA</p>
            <h3>BlackPearl is ready</h3>
            <dl>
						<div><dt>Plex library</dt><dd>{selectedItems.length} {selectedItems.length === 1 ? "video" : "videos"}</dd></div>
						<div><dt>Manifest</dt><dd>{selectedItems.map(manifestLabel).join(" · ")}</dd></div>
						<div><dt>Logical size</dt><dd>{formatBytes(selectedItems.reduce((total, item) => total + item.size, 0))}</dd></div>
              <div><dt>Storage</dt><dd>Rolling range cache</dd></div>
            </dl>
            <div className="actions">
              <a className="primary button-link" href="http://localhost:32402/web" target="_blank" rel="noreferrer">Open Plex</a>
              {canUseSavedToken
                ? <button type="button" onClick={() => void findVideos(true)} disabled={pending}>Change video</button>
                : <button type="button" onClick={() => { setToken(""); setPhase("credentials"); setMessage("Re-enter your saved TorBox token to authorize this browser."); }}>Change video</button>}
              <button type="button" onClick={() => { setToken(""); setShowToken(false); setPhase("credentials"); setMessage("Enter a replacement TorBox token."); }}>Replace token</button>
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

function suggestDraft(candidate: MediaCandidate, forcedType?: "movie" | "episode"): SelectionDraft {
	const base = (candidate.name.split("/").at(-1) ?? candidate.name).replace(/\.(mkv|mp4)$/i, "");
	const episodeMatch = /^(.*?)[ ._-]+S(\d{1,2})E(\d{1,3})(?:[ ._-]+(.*))?$/i.exec(base);
	const mediaType = forcedType ?? (episodeMatch ? "episode" : "movie");
	if (mediaType === "episode") {
		const showSource = episodeMatch?.[1] ?? base;
		const year = suggestYear(candidate.name);
		const showTitle = cleanDisplayTitle(showSource.replace(/\b(19|20)\d{2}\b/, "")).replace(/\s*-\s*$/, "");
		const season = episodeMatch ? Number.parseInt(episodeMatch[2], 10) : 1;
		const episode = episodeMatch ? Number.parseInt(episodeMatch[3], 10) : 1;
		return {
			objectId: candidate.objectId, mediaType, showTitle, year, season, episode,
			title: suggestEpisodeTitle(episodeMatch?.[4], episode),
		};
	}
	return {
		objectId: candidate.objectId, mediaType, title: suggestTitle(candidate.name), year: suggestYear(base),
		showTitle: "", season: 1, episode: 1,
	};
}

function suggestYear(value: string): number {
	const matched = /\b(19|20)\d{2}\b/.exec(value);
	return matched ? Number.parseInt(matched[0], 10) : new Date().getFullYear();
}

function cleanDisplayTitle(value: string): string {
	return value.replace(/[._]+/g, " ").replace(/\s+/g, " ").trim();
}

function suggestEpisodeTitle(value: string | undefined, episode: number): string {
	const fallback = `Episode ${String(episode).padStart(2, "0")}`;
	if (!value) return fallback;
	const cleaned = cleanDisplayTitle(value.replace(/\s*\[[^\]]*\]\s*$/, "")).replace(/^\s*-\s*/, "");
	if (cleaned === "" || /^(?:\d{3,4}p|UHD|WEB(?:-DL)?|BluRay|HDR|DV|x26[45]|h26[45])\b/i.test(cleaned)) return fallback;
	return cleaned;
}

function mediaTypeFromValue(value: string): "movie" | "episode" {
	return value === "episode" ? "episode" : "movie";
}

function validDraft(draft: SelectionDraft): boolean {
	if (draft.title.trim() === "" || !Number.isInteger(draft.year) || draft.year < 1888 || draft.year > 2100) return false;
	if (draft.mediaType === "movie") return true;
	return draft.showTitle.trim() !== ""
		&& Number.isInteger(draft.season) && draft.season >= 0 && draft.season <= 99
		&& Number.isInteger(draft.episode) && draft.episode >= 1 && draft.episode <= 999;
}

function toApplyItem(draft: SelectionDraft): ApplyItemInput {
	if (draft.mediaType === "episode") {
		return {
			objectId: draft.objectId, mediaType: draft.mediaType, title: draft.title, year: draft.year,
			showTitle: draft.showTitle, season: draft.season, episode: draft.episode,
		};
	}
	return { objectId: draft.objectId, mediaType: draft.mediaType, title: draft.title, year: draft.year };
}

function manifestLabel(item: SetupConfiguration): string {
	if (item.mediaType === "episode") {
		return `${item.showTitle ?? "TV Show"} S${String(item.season ?? 0).padStart(2, "0")}E${String(item.episode ?? 0).padStart(2, "0")}`;
	}
	return `${item.title} (${item.year})`;
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
