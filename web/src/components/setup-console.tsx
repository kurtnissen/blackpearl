"use client";

import { useEffect, useMemo, useState } from "react";
import {
  acquireMedia,
  applyConfiguration,
  configureAcquisition,
  discoverMedia,
  getAcquisitionStatus,
  getStatus,
  getWatchlistStatus,
  SetupAPIError,
  type AcquisitionIntent,
  type MediaCandidate,
	type ApplyItemInput,
  type SetupAuthorization,
  type SetupConfiguration,
  type WatchlistStatus,
} from "../lib/api";

type Phase = "loading" | "credentials" | "select" | "ready";
type AcquisitionView = "closed" | "loading" | "settings" | "search";
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
  const [acquisitionView, setAcquisitionView] = useState<AcquisitionView>("closed");
  const [prowlarrURL, setProwlarrURL] = useState("http://prowlarr:9696");
  const [prowlarrKey, setProwlarrKey] = useState("");
  const [showProwlarrKey, setShowProwlarrKey] = useState(false);
  const [acquisitionType, setAcquisitionType] = useState<"movie" | "episode">("movie");
  const [acquisitionTitle, setAcquisitionTitle] = useState("");
  const [acquisitionYear, setAcquisitionYear] = useState(new Date().getFullYear());
  const [acquisitionSeason, setAcquisitionSeason] = useState(1);
  const [acquisitionEpisode, setAcquisitionEpisode] = useState(1);
  const [watchlistStatus, setWatchlistStatus] = useState<WatchlistStatus | null>(null);
  const [watchlistLoading, setWatchlistLoading] = useState(false);
  const [watchlistError, setWatchlistError] = useState("");
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
          if (storedAuthorization.session || storedAuthorization.bootstrap) {
            setWatchlistLoading(true);
            getWatchlistStatus(status.csrfToken, storedAuthorization)
              .then((result) => {
                if (!active) return;
                setWatchlistStatus(result);
                setWatchlistError("");
              })
              .catch((error: unknown) => {
                if (!active) return;
                setWatchlistError(publicMessage(error));
              })
              .finally(() => {
                if (active) setWatchlistLoading(false);
              });
          }
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

  async function refreshWatchlist(): Promise<void> {
    setWatchlistLoading(true);
    setWatchlistError("");
    try {
      setWatchlistStatus(await getWatchlistStatus(csrf, authorization));
    } catch (error: unknown) {
      setWatchlistError(publicMessage(error));
    } finally {
      setWatchlistLoading(false);
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

  async function openAcquisition(): Promise<void> {
    setAcquisitionView("loading");
    setPending(true);
    setMessage("Checking the search connection…");
    try {
      const status = await getAcquisitionStatus();
      setAcquisitionView(status.configured ? "search" : "settings");
      setMessage(status.configured
        ? "Prowlarr is connected. Enter a movie or episode."
        : "Connect Prowlarr once to search your configured indexers.");
    } catch (error: unknown) {
      setAcquisitionView("closed");
      setMessage(publicMessage(error));
    } finally {
      setPending(false);
    }
  }

  async function connectProwlarr(): Promise<void> {
    setPending(true);
    setMessage("Checking the Prowlarr connection…");
    try {
      const result = await configureAcquisition(
        { baseUrl: prowlarrURL, apiKey: prowlarrKey },
        csrf,
        authorization,
      );
      storeSession(result.session);
      setSession(result.session);
      setProwlarrKey("");
      setShowProwlarrKey(false);
      setAcquisitionView("search");
      setMessage("Prowlarr is connected. Enter a movie or episode.");
    } catch (error: unknown) {
      setMessage(publicMessage(error));
    } finally {
      setPending(false);
    }
  }

  async function addRequestedMedia(): Promise<void> {
    if (!validAcquisitionRequest(acquisitionTitle, acquisitionYear, acquisitionType, acquisitionSeason, acquisitionEpisode)) return;
    setPending(true);
    setMessage(`Looking for an instant ${acquisitionType === "movie" ? "movie" : "episode"}…`);
    try {
      const result = await acquireMedia(
        acquisitionIntent(acquisitionType, acquisitionTitle, acquisitionYear, acquisitionSeason, acquisitionEpisode),
        csrf,
        authorization,
      );
      storeSession(result.session);
      setSession(result.session);
      setSelectedItems(result.selectedItems);
      setAcquisitionTitle("");
      setMessage(`Added ${result.selected.title} to Plex. Scan the library if it does not appear automatically.`);
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
              <button type="button" onClick={() => void openAcquisition()} disabled={pending}>Find something new</button>
              {canUseSavedToken
                ? <button type="button" onClick={() => void findVideos(true)} disabled={pending}>Change video</button>
                : <button type="button" onClick={() => { setToken(""); setPhase("credentials"); setMessage("Re-enter your saved TorBox token to authorize this browser."); }}>Change video</button>}
              <button type="button" onClick={() => { setToken(""); setShowToken(false); setPhase("credentials"); setMessage("Enter a replacement TorBox token."); }}>Replace token</button>
            </div>
            <section className="watchlist-panel" aria-labelledby="watchlist-title">
              <div className="watchlist-panel__heading">
                <div>
                  <p className="ready-kicker">AUTOMATIC INTAKE</p>
                  <h4 id="watchlist-title">Plex Watchlist</h4>
                </div>
                {watchlistStatus && (
                  <span className={`watchlist-badge watchlist-badge--${watchlistStatus.healthy ? "healthy" : "attention"}`}>
                    {watchlistStatus.healthy
                      ? (watchlistStatus.acquisitionEnabled ? "AUTO ADD ON" : "OBSERVING")
                      : "NEEDS ATTENTION"}
                  </span>
                )}
              </div>
              {watchlistLoading && <div className="loading-rule" aria-label="Refreshing Plex Watchlist" />}
              {watchlistStatus && (
                <>
                  <p className="watchlist-summary">{watchlistSummary(watchlistStatus)}</p>
                  <div className="watchlist-stats">
                    <p><strong>{waitingMovieCount(watchlistStatus)} movies waiting</strong><span>Queued or awaiting a cached match</span></p>
                    <p><strong>{watchlistStatus.queue.observedShows} shows observed</strong><span>Tracked safely; episode intake comes later</span></p>
                    <p><strong>{watchlistStatus.queue.succeeded} added automatically</strong><span>Published into the BlackPearl manifest</span></p>
                    <p><strong>{watchlistStatus.queue.manualReview} need review</strong><span>Held instead of making an unsafe guess</span></p>
                  </div>
                  {watchlistStatus.lastSyncAt && <p className="watchlist-sync">Last checked {formatWatchlistTime(watchlistStatus.lastSyncAt)}</p>}
                </>
              )}
              {!watchlistStatus && !watchlistLoading && !watchlistError && (
                <p className="watchlist-summary">Pair this browser from the BlackPearl launcher to see Watchlist activity.</p>
              )}
              {watchlistError && <p className="watchlist-error">Watchlist status is temporarily unavailable. Your existing Plex library is unaffected.</p>}
              <button type="button" onClick={() => void refreshWatchlist()} disabled={watchlistLoading || (!session && !bootstrap)}>Refresh Watchlist</button>
            </section>
            {acquisitionView !== "closed" && (
              <section className="acquisition-panel" aria-labelledby="acquisition-title">
                <div className="acquisition-panel__heading">
                  <h4 id="acquisition-title">Add to Plex</h4>
                  <button type="button" onClick={() => setAcquisitionView("closed")} disabled={pending}>Close</button>
                </div>

                {acquisitionView === "loading" && <div className="loading-rule" aria-hidden="true" />}

                {acquisitionView === "settings" && (
                  <form className="provider-form" onSubmit={(event) => { event.preventDefault(); void connectProwlarr(); }}>
                    <label>Prowlarr URL<input type="url" value={prowlarrURL} maxLength={2048} onChange={(event) => setProwlarrURL(event.target.value)} required disabled={pending} /></label>
                    <label htmlFor="prowlarr-api-key">Prowlarr API key</label>
                    <p className="field-note">Copy the API key from Prowlarr Settings. BlackPearl stores it privately and never displays it again.</p>
                    <div className="token-input">
                      <input id="prowlarr-api-key" type={showProwlarrKey ? "text" : "password"} value={prowlarrKey} maxLength={4096} autoComplete="new-password" onChange={(event) => setProwlarrKey(event.target.value)} required disabled={pending} />
                      <button type="button" aria-pressed={showProwlarrKey} onClick={() => setShowProwlarrKey((visible) => !visible)} disabled={pending}>{showProwlarrKey ? "Hide key" : "Show key"}</button>
                    </div>
                    <div className="actions"><button className="primary" type="submit" disabled={pending || prowlarrKey.length === 0}>Connect Prowlarr</button></div>
                  </form>
                )}

                {acquisitionView === "search" && (
                  <form className="acquisition-form" onSubmit={(event) => { event.preventDefault(); void addRequestedMedia(); }}>
                    <label>Media type<select value={acquisitionType} onChange={(event) => setAcquisitionType(mediaTypeFromValue(event.target.value))} disabled={pending}><option value="movie">Movie</option><option value="episode">TV episode</option></select></label>
                    <label className="acquisition-form__title">Title<input value={acquisitionTitle} maxLength={200} onChange={(event) => setAcquisitionTitle(event.target.value)} required disabled={pending} /></label>
                    <label>Year<input type="number" min="1888" max="2100" value={acquisitionYear} onChange={(event) => setAcquisitionYear(event.target.valueAsNumber)} required disabled={pending} /></label>
                    {acquisitionType === "episode" && <label>Season<input type="number" min="0" max="99" value={acquisitionSeason} onChange={(event) => setAcquisitionSeason(event.target.valueAsNumber)} required disabled={pending} /></label>}
                    {acquisitionType === "episode" && <label>Episode<input type="number" min="1" max="999" value={acquisitionEpisode} onChange={(event) => setAcquisitionEpisode(event.target.valueAsNumber)} required disabled={pending} /></label>}
                    <div className="acquisition-form__actions actions">
                      <button className="primary" type="submit" disabled={pending || !validAcquisitionRequest(acquisitionTitle, acquisitionYear, acquisitionType, acquisitionSeason, acquisitionEpisode)}>Find and add to Plex</button>
                      <button type="button" onClick={() => setAcquisitionView("settings")} disabled={pending}>Change Prowlarr</button>
                    </div>
                  </form>
                )}
              </section>
            )}
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

function waitingMovieCount(status: WatchlistStatus): number {
  return status.queue.pendingMovies + status.queue.acquiring + status.queue.notCached + status.queue.retryable;
}

function watchlistSummary(status: WatchlistStatus): string {
  if (!status.enabled) return "Plex Watchlist observation is turned off.";
  if (!status.healthy) return "BlackPearl could not read Plex Watchlist during its latest check.";
  if (status.acquisitionEnabled) return "BlackPearl is watching Plex and can add authorized cached matches automatically.";
  return "BlackPearl is watching Plex. Automatic adding stays off until your authorized Prowlarr indexers are ready.";
}

function formatWatchlistTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
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

function validAcquisitionRequest(title: string, year: number, mediaType: "movie" | "episode", season: number, episode: number): boolean {
  if (title.trim() === "" || !Number.isInteger(year) || year < 1888 || year > 2100) return false;
  if (mediaType === "movie") return true;
  return Number.isInteger(season) && season >= 0 && season <= 99
    && Number.isInteger(episode) && episode >= 1 && episode <= 999;
}

function acquisitionIntent(mediaType: "movie" | "episode", title: string, year: number, season: number, episode: number): AcquisitionIntent {
  if (mediaType === "episode") return { mediaType, title: title.trim(), year, season, episode };
  return { mediaType, title: title.trim(), year };
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
