export type MediaCandidate = {
  objectId: string;
  name: string;
  extension: ".mp4" | ".mkv";
  size: number;
};

export type SetupConfiguration = {
  objectId: string;
  name: string;
  extension: ".mp4" | ".mkv";
  size: number;
	mediaType: "movie" | "episode";
  title: string;
  year: number;
	showTitle?: string;
	season?: number;
	episode?: number;
};

export type SetupStatus = {
  setupRequired: boolean;
  tokenConfigured: boolean;
  csrfToken: string;
  selected?: SetupConfiguration;
	selectedItems?: SetupConfiguration[];
};

export type ApplyItemInput = {
	objectId: string;
	mediaType: "movie" | "episode";
	title: string;
	year: number;
	showTitle?: string;
	season?: number;
	episode?: number;
};

export type ApplyInput = {
  token?: string;
	items: ApplyItemInput[];
};

export type SetupAuthorization = {
  session?: string;
  bootstrap?: string;
};

export type DiscoveryResult = {
  candidates: MediaCandidate[];
  session: string;
};

export type ApplyResult = {
  selected: SetupConfiguration;
	selectedItems: SetupConfiguration[];
  session: string;
};

export type AcquisitionStatus = {
  configured: boolean;
};

export type WatchlistQueueStatus = {
  pendingMovies: number;
  acquiring: number;
  succeeded: number;
  notCached: number;
  retryable: number;
  manualReview: number;
  observedShows: number;
};

export type WatchlistStatus = {
  enabled: boolean;
  healthy: boolean;
  acquisitionEnabled: boolean;
  lastSyncAt?: string;
  queue: WatchlistQueueStatus;
};

export type WatchlistStatusResult = WatchlistStatus & {
  session: string;
};

export type ProwlarrSettingsInput = {
  baseUrl: string;
  apiKey: string;
};

export type AcquisitionIntent =
  | { mediaType: "movie"; title: string; year: number }
  | { mediaType: "episode"; title: string; year: number; season: number; episode: number };

export type AcquisitionStatusResult = AcquisitionStatus & {
  session: string;
};

export type AcquisitionJobState = "queued" | "selected" | "preparing" | "succeeded" | "failed" | "manual_review";

export type AcquisitionJobError = "provider_unavailable" | "unauthorized" | "no_release" | "no_playable_media" | "materialization_failed" | "stalled" | "ambiguous_mutation";

export type AcquisitionJob = {
	id: string;
	state: AcquisitionJobState;
	mediaType: "movie" | "episode";
	title: string;
	year: number;
	season?: number;
	episode?: number;
	progress: number;
	errorCode?: AcquisitionJobError;
	createdAt: string;
	updatedAt: string;
};

export type AcquisitionJobSubmission = {
	job: AcquisitionJob;
	created: boolean;
	session: string;
};

export class SetupAPIError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "SetupAPIError";
    this.code = code;
  }
}

export async function getStatus(): Promise<SetupStatus> {
  const response = await fetch("/api/setup/status", { method: "GET", cache: "no-store" });
  return readJSON(response, isSetupStatus, "invalid_status");
}

export async function discoverMedia(token: string, csrfToken: string, authorization: SetupAuthorization): Promise<DiscoveryResult> {
  const response = await fetch("/api/setup/discover", {
    method: "POST",
    cache: "no-store",
    headers: mutationHeaders(csrfToken, authorization),
    body: JSON.stringify(token === "" ? {} : { token }),
  });
  const envelope = await readJSON(response, isCandidateEnvelope, "invalid_candidates");
  return { candidates: envelope.candidates, session: readSession(response) };
}

export async function applyConfiguration(input: ApplyInput, csrfToken: string, authorization: SetupAuthorization): Promise<ApplyResult> {
  const response = await fetch("/api/setup/configuration", {
    method: "PUT",
    cache: "no-store",
    headers: mutationHeaders(csrfToken, authorization),
    body: JSON.stringify(input),
  });
  const envelope = await readJSON(response, isSelectionEnvelope, "invalid_configuration");
	return { selected: envelope.selected, selectedItems: envelope.selectedItems, session: readSession(response) };
}

export async function getAcquisitionStatus(): Promise<AcquisitionStatus> {
  const response = await fetch("/api/acquisition/status", { method: "GET", cache: "no-store" });
  return readJSON(response, isAcquisitionStatus, "invalid_acquisition_status");
}

export async function getWatchlistStatus(
  csrfToken: string,
  authorization: SetupAuthorization,
): Promise<WatchlistStatus> {
  const response = await fetch("/api/watchlist/status", {
    method: "GET",
    cache: "no-store",
    headers: mutationHeaders(csrfToken, authorization),
  });
  return readJSON(response, isWatchlistStatus, "invalid_watchlist_status");
}

export async function setWatchlistAcquisitionEnabled(
  acquisitionEnabled: boolean,
  csrfToken: string,
  authorization: SetupAuthorization,
): Promise<WatchlistStatusResult> {
  const response = await fetch("/api/watchlist/settings", {
    method: "PUT",
    cache: "no-store",
    headers: mutationHeaders(csrfToken, authorization),
    body: JSON.stringify({ acquisitionEnabled }),
  });
  const status = await readJSON(response, isWatchlistStatus, "invalid_watchlist_status");
  return { ...status, session: readSession(response) };
}

export async function configureAcquisition(
  input: ProwlarrSettingsInput,
  csrfToken: string,
  authorization: SetupAuthorization,
): Promise<AcquisitionStatusResult> {
  const response = await fetch("/api/acquisition/settings", {
    method: "PUT",
    cache: "no-store",
    headers: mutationHeaders(csrfToken, authorization),
    body: JSON.stringify(input),
  });
  const status = await readJSON(response, isAcquisitionStatus, "invalid_acquisition_status");
  return { ...status, session: readSession(response) };
}

export async function acquireMedia(
  input: AcquisitionIntent,
  csrfToken: string,
  authorization: SetupAuthorization,
): Promise<ApplyResult> {
  const response = await fetch("/api/acquisition/acquire", {
    method: "POST",
    cache: "no-store",
    headers: mutationHeaders(csrfToken, authorization),
    body: JSON.stringify(input),
  });
  const envelope = await readJSON(response, isSelectionEnvelope, "invalid_acquisition");
  return { selected: envelope.selected, selectedItems: envelope.selectedItems, session: readSession(response) };
}

export async function submitAcquisitionJob(
	input: AcquisitionIntent,
	csrfToken: string,
	authorization: SetupAuthorization,
): Promise<AcquisitionJobSubmission> {
	const response = await fetch("/api/acquisition/jobs", {
		method: "POST",
		cache: "no-store",
		headers: mutationHeaders(csrfToken, authorization),
		body: JSON.stringify(input),
	});
	const envelope = await readJSON(response, isAcquisitionJobSubmission, "invalid_acquisition_job");
	return { ...envelope, session: readSession(response) };
}

export async function listAcquisitionJobs(
	csrfToken: string,
	authorization: SetupAuthorization,
): Promise<AcquisitionJob[]> {
	const response = await fetch("/api/acquisition/jobs", {
		method: "GET",
		cache: "no-store",
		headers: mutationHeaders(csrfToken, authorization),
	});
	const envelope = await readJSON(response, isAcquisitionJobList, "invalid_acquisition_job_list");
	return envelope.jobs;
}

export async function getAcquisitionJob(
	id: string,
	csrfToken: string,
	authorization: SetupAuthorization,
): Promise<AcquisitionJob> {
	const response = await fetch(`/api/acquisition/jobs/${encodeURIComponent(id)}`, {
		method: "GET",
		cache: "no-store",
		headers: mutationHeaders(csrfToken, authorization),
	});
	return readJSON(response, isAcquisitionJob, "invalid_acquisition_job");
}

function mutationHeaders(csrfToken: string, authorization: SetupAuthorization): Record<string, string> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "X-BlackPearl-CSRF": csrfToken,
  };
  if (authorization.session) headers["X-BlackPearl-Session"] = authorization.session;
  if (authorization.bootstrap) headers["X-BlackPearl-Bootstrap"] = authorization.bootstrap;
  return headers;
}

function readSession(response: Response): string {
  const session = response.headers.get("X-BlackPearl-Session") ?? "";
  if (!/^[0-9a-f]{64}$/.test(session)) {
    throw new SetupAPIError("invalid_session", "BlackPearl returned an invalid setup authorization response.");
  }
  return session;
}

async function readJSON<T>(response: Response, validate: (value: unknown) => value is T, fallbackCode: string): Promise<T> {
  const value: unknown = await response.json();
  if (!response.ok) {
    if (isErrorEnvelope(value)) {
      throw new SetupAPIError(value.code, value.message);
    }
    throw new SetupAPIError("request_failed", "BlackPearl could not complete that request.");
  }
  if (!validate(value)) {
    throw new SetupAPIError(fallbackCode, "BlackPearl returned an unexpected response.");
  }
  return value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isErrorEnvelope(value: unknown): value is { code: string; message: string } {
  return isRecord(value) && typeof value.code === "string" && typeof value.message === "string";
}

function isCandidate(value: unknown): value is MediaCandidate {
  return isRecord(value)
    && typeof value.objectId === "string"
    && typeof value.name === "string"
    && (value.extension === ".mp4" || value.extension === ".mkv")
    && typeof value.size === "number";
}

function isConfiguration(value: unknown): value is SetupConfiguration {
  return isRecord(value)
    && typeof value.objectId === "string"
    && typeof value.name === "string"
    && (value.extension === ".mp4" || value.extension === ".mkv")
    && typeof value.size === "number"
		&& (value.mediaType === "movie" || value.mediaType === "episode")
    && typeof value.title === "string"
		&& typeof value.year === "number"
		&& (value.showTitle === undefined || typeof value.showTitle === "string")
		&& (value.season === undefined || typeof value.season === "number")
		&& (value.episode === undefined || typeof value.episode === "number");
}

function isSetupStatus(value: unknown): value is SetupStatus {
  return isRecord(value)
    && typeof value.setupRequired === "boolean"
    && typeof value.tokenConfigured === "boolean"
    && typeof value.csrfToken === "string"
		&& (value.selected === undefined || isConfiguration(value.selected))
		&& (value.selectedItems === undefined || (Array.isArray(value.selectedItems) && value.selectedItems.every(isConfiguration)));
}

function isCandidateEnvelope(value: unknown): value is { candidates: MediaCandidate[] } {
  return isRecord(value) && Array.isArray(value.candidates) && value.candidates.every(isCandidate);
}

function isSelectionEnvelope(value: unknown): value is { selected: SetupConfiguration; selectedItems: SetupConfiguration[] } {
	return isRecord(value)
		&& isConfiguration(value.selected)
		&& Array.isArray(value.selectedItems)
		&& value.selectedItems.length > 0
		&& value.selectedItems.every(isConfiguration);
}

function isAcquisitionStatus(value: unknown): value is AcquisitionStatus {
  return isRecord(value) && typeof value.configured === "boolean";
}

function isWatchlistStatus(value: unknown): value is WatchlistStatus {
  return isRecord(value)
    && typeof value.enabled === "boolean"
    && typeof value.healthy === "boolean"
    && typeof value.acquisitionEnabled === "boolean"
    && (value.lastSyncAt === undefined
      || (typeof value.lastSyncAt === "string" && Number.isFinite(Date.parse(value.lastSyncAt))))
    && isWatchlistQueueStatus(value.queue);
}

function isWatchlistQueueStatus(value: unknown): value is WatchlistQueueStatus {
  return isRecord(value)
    && isCount(value.pendingMovies)
    && isCount(value.acquiring)
    && isCount(value.succeeded)
    && isCount(value.notCached)
    && isCount(value.retryable)
    && isCount(value.manualReview)
    && isCount(value.observedShows);
}

function isAcquisitionJob(value: unknown): value is AcquisitionJob {
	if (!isRecord(value)
		|| typeof value.id !== "string" || !/^[0-9a-f]{32}$/.test(value.id)
		|| !isAcquisitionJobState(value.state)
		|| (value.mediaType !== "movie" && value.mediaType !== "episode")
		|| typeof value.title !== "string" || value.title.length === 0 || value.title.length > 200
		|| typeof value.year !== "number" || !Number.isInteger(value.year) || value.year < 1888 || value.year > 2100
		|| !isCount(value.progress) || value.progress > 100
		|| typeof value.createdAt !== "string" || !Number.isFinite(Date.parse(value.createdAt))
		|| typeof value.updatedAt !== "string" || !Number.isFinite(Date.parse(value.updatedAt))
		|| (value.errorCode !== undefined && !isAcquisitionJobError(value.errorCode))) {
		return false;
	}
	if (value.mediaType === "episode") {
		return typeof value.season === "number" && Number.isInteger(value.season) && value.season >= 0 && value.season <= 99
			&& typeof value.episode === "number" && Number.isInteger(value.episode) && value.episode >= 1 && value.episode <= 999;
	}
	return value.season === undefined && value.episode === undefined;
}

function isAcquisitionJobState(value: unknown): value is AcquisitionJobState {
	return value === "queued" || value === "selected" || value === "preparing"
		|| value === "succeeded" || value === "failed" || value === "manual_review";
}

function isAcquisitionJobError(value: unknown): value is AcquisitionJobError {
	return value === "provider_unavailable" || value === "unauthorized" || value === "no_release"
		|| value === "no_playable_media" || value === "materialization_failed" || value === "stalled" || value === "ambiguous_mutation";
}

function isAcquisitionJobSubmission(value: unknown): value is { job: AcquisitionJob; created: boolean } {
	return isRecord(value) && isAcquisitionJob(value.job) && typeof value.created === "boolean";
}

function isAcquisitionJobList(value: unknown): value is { jobs: AcquisitionJob[] } {
	return isRecord(value) && Array.isArray(value.jobs) && value.jobs.length <= 20 && value.jobs.every(isAcquisitionJob);
}

function isCount(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}
