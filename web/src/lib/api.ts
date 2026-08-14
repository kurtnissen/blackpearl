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
};

export type ApplyInput = {
  token?: string;
  objectId: string;
  title: string;
  year: number;
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

export async function discoverMedia(token: string, csrfToken: string): Promise<MediaCandidate[]> {
  const response = await fetch("/api/setup/discover", {
    method: "POST",
    cache: "no-store",
    headers: mutationHeaders(csrfToken),
    body: JSON.stringify(token === "" ? {} : { token }),
  });
  const envelope = await readJSON(response, isCandidateEnvelope, "invalid_candidates");
  return envelope.candidates;
}

export async function applyConfiguration(input: ApplyInput, csrfToken: string): Promise<SetupConfiguration> {
  const response = await fetch("/api/setup/configuration", {
    method: "PUT",
    cache: "no-store",
    headers: mutationHeaders(csrfToken),
    body: JSON.stringify(input),
  });
  const envelope = await readJSON(response, isSelectionEnvelope, "invalid_configuration");
  return envelope.selected;
}

function mutationHeaders(csrfToken: string): Record<string, string> {
  return { "Content-Type": "application/json", "X-BlackPearl-CSRF": csrfToken };
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
    && (value.selected === undefined || isConfiguration(value.selected));
}

function isCandidateEnvelope(value: unknown): value is { candidates: MediaCandidate[] } {
  return isRecord(value) && Array.isArray(value.candidates) && value.candidates.every(isCandidate);
}

function isSelectionEnvelope(value: unknown): value is { selected: SetupConfiguration } {
  return isRecord(value) && isConfiguration(value.selected);
}
