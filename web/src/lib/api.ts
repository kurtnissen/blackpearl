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
  title: string;
  year: number;
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
	title: string;
	year: number;
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
    && typeof value.title === "string"
    && typeof value.year === "number";
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
