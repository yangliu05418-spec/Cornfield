const TENANT_SCOPE_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

let activeTenantScope: string | null = null;

export function isEmbeddedDirectorDeskRuntime() {
  try {
    return new URLSearchParams(window.location.search).get("embedded") === "1";
  } catch {
    return false;
  }
}

export function setDirectorTenantScope(value: unknown) {
  const scope = typeof value === "string" ? value.trim().toLowerCase() : "";
  activeTenantScope = TENANT_SCOPE_PATTERN.test(scope) ? scope : null;
  return activeTenantScope;
}

export function getDirectorTenantScope() {
  return activeTenantScope;
}

export function getDirectorTenantStoragePrefix() {
  if (activeTenantScope) return `user:${activeTenantScope}:`;
  return isEmbeddedDirectorDeskRuntime() ? null : "";
}

export function getDirectorTenantLocalStorageKey(baseKey: string) {
  if (activeTenantScope) return `${baseKey}:user:${activeTenantScope}`;
  return isEmbeddedDirectorDeskRuntime() ? null : baseKey;
}
