import type { SyntheticEvent } from "react";
import {
  HealthState,
  type GetHarnessCatalogResponse,
  type HarnessProviderInfo,
  type Project,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import {
  capabilityEntries,
  healthLabel,
  providerSelectable,
  sameSelection,
  selectionFromProject,
  type HarnessSelection,
} from "./model.js";

export type CatalogState = "loading" | "ready" | "error";

interface HarnessSettingsProps {
  project: Project;
  catalog?: GetHarnessCatalogResponse | undefined;
  catalogState: CatalogState;
  catalogError?: string | undefined;
  draft: HarnessSelection;
  saving: boolean;
  feedback?: string | undefined;
  feedbackIsError?: boolean | undefined;
  onSelectionChange: (selection: HarnessSelection) => void;
  onSave: () => void;
  onRetry: () => void;
}

export function HarnessSettings({
  project,
  catalog,
  catalogState,
  catalogError,
  draft,
  saving,
  feedback,
  feedbackIsError,
  onSelectionChange,
  onSave,
  onRetry,
}: HarnessSettingsProps) {
  const saved = selectionFromProject(project);
  const currentProviderId = project.harnessBinding?.providerId.trim() ?? "";
  const providers = catalog?.providers ?? [];
  const currentHasNoCatalogEntry =
    currentProviderId !== "" && !providers.some((provider) => provider.id === currentProviderId);
  const currentHealthLabel =
    catalogState === "ready"
      ? "unknown"
      : catalogState === "loading"
        ? "health loading"
        : "catalog unavailable";
  const draftProvider =
    draft.kind === "provider"
      ? providers.find((provider) => provider.id === draft.providerId)
      : undefined;
  const draftCanSave =
    draft.kind === "global" ||
    (catalogState === "ready" &&
      draftProvider !== undefined &&
      providerSelectable(draftProvider.health));

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    onSave();
  }

  return (
    <section className="harness-settings" aria-labelledby="harness-settings-title">
      <header>
        <div>
          <p>PROJECT SETTINGS</p>
          <h2 id="harness-settings-title">Harness provider</h2>
        </div>
        <span>revision {project.revision.toString()}</span>
      </header>

      <form onSubmit={submit}>
        <fieldset disabled={saving}>
          <legend>Provider selection</legend>
          <label className="provider-option global-default">
            <input
              aria-label="Use Global Default"
              checked={draft.kind === "global"}
              name="harness-provider"
              onChange={() => {
                onSelectionChange({ kind: "global" });
              }}
              type="radio"
            />
            <span>
              <strong>Use Global Default</strong>
              <small>
                Core default · {catalog?.defaultProviderId || "configured by the server"}
              </small>
            </span>
          </label>

          {catalogState === "loading" ? (
            <p className="catalog-state" role="status">
              Loading provider catalog…
            </p>
          ) : null}

          {catalogState === "error" ? (
            <div className="catalog-state catalog-error" role="alert">
              <p>{catalogError || "Provider catalog is unavailable."}</p>
              <Button onClick={onRetry} type="button">
                Retry catalog
              </Button>
            </div>
          ) : null}

          {currentHasNoCatalogEntry ? (
            <label className="provider-option current-unknown">
              <input
                aria-label={`Current provider ${currentProviderId}`}
                checked={draft.kind === "provider" && draft.providerId === currentProviderId}
                disabled
                name="harness-provider"
                readOnly
                type="radio"
              />
              <span>
                <strong>{currentProviderId}</strong>
                <small>Current binding · {currentHealthLabel}</small>
                <em>
                  {catalogState === "ready"
                    ? "Not reported by the current provider catalog. You can still use Global Default."
                    : "The saved binding is retained. You can still use Global Default."}
                </em>
              </span>
            </label>
          ) : null}

          {catalogState === "ready" && providers.length === 0 ? (
            <p className="catalog-state">No providers were reported.</p>
          ) : null}

          {catalogState === "ready"
            ? providers.map((provider) => (
                <ProviderOption
                  current={provider.id === currentProviderId}
                  defaultProvider={provider.id === catalog?.defaultProviderId}
                  key={provider.id}
                  provider={provider}
                  selected={draft.kind === "provider" && draft.providerId === provider.id}
                  onSelect={() => {
                    onSelectionChange({ kind: "provider", providerId: provider.id });
                  }}
                />
              ))
            : null}
        </fieldset>

        {feedback ? (
          <p
            className={feedbackIsError ? "binding-feedback error" : "binding-feedback"}
            role={feedbackIsError ? "alert" : "status"}
          >
            {feedback}
          </p>
        ) : null}

        <Button disabled={saving || !draftCanSave || sameSelection(saved, draft)} type="submit">
          {saving ? "Saving…" : "Save harness setting"}
        </Button>
      </form>
    </section>
  );
}

function ProviderOption({
  provider,
  selected,
  current,
  defaultProvider,
  onSelect,
}: {
  provider: HarnessProviderInfo;
  selected: boolean;
  current: boolean;
  defaultProvider: boolean;
  onSelect: () => void;
}) {
  const health = healthLabel(provider.health);
  const selectable = providerSelectable(provider.health);
  return (
    <label className={`provider-option health-${health}`}>
      <input
        aria-label={`Select ${provider.displayName}`}
        checked={selected}
        disabled={!selectable}
        name="harness-provider"
        onChange={onSelect}
        type="radio"
      />
      <span>
        <strong>{provider.displayName}</strong>
        <small>
          {provider.id} · adapter {provider.adapterVersion}
        </small>
        <span className="provider-badges">
          <b>{health}</b>
          {current ? <b>current</b> : null}
          {defaultProvider ? <b>global default</b> : null}
        </span>
        {provider.unavailableReason ? <em>{provider.unavailableReason}</em> : null}
        {provider.health === HealthState.DEGRADED ? (
          <em>Degraded providers can be selected with caution.</em>
        ) : null}
        <ul className="capability-list" aria-label={`${provider.displayName} capabilities`}>
          {capabilityEntries(provider.capabilities).map((capability) => (
            <li key={capability.id}>
              <span>{capability.label}</span>
              <b>{capability.available ? "available" : "unavailable"}</b>
            </li>
          ))}
        </ul>
      </span>
    </label>
  );
}
