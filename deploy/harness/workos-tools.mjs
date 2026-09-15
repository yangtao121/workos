// WorkOS tools plugin for the official DeepSeek runtime (B04).
//
// This is a thin cordis plugin loaded by the pinned runtime as a relative
// row (`./workos-tools.mjs`) from the generated per-session cordis.yml. It
// registers the minimal read-only WorkOS tool set of this slice:
//
//   - workos_project_info   current project facts
//   - workos_list_artifacts bounded artifact listing of the current project
//
// The module deliberately imports NOTHING. The packaged runtime resolves
// bare package specifiers (e.g. '@deepseek-ai/schemastery') only for modules
// inside its own closure; an external configuration-relative plugin file
// cannot import them. Plain language built-ins (fetch, JSON, Promise) are
// available, so the plugin is dependency-free by construction.
//
// Authorization honesty: both tools only READ. The current owner and project
// are derived exclusively from the session child environment
// (WORKOS_TOOL_OWNER_ID / WORKOS_TOOL_PROJECT_ID) injected by the harness
// host from the active task lease — the model can never submit an owner,
// project, or artifact id to widen the read scope. Core authorizes each
// Connect call with the owner-scoped identity headers. Missing environment
// facts fail the tool call (never the process), Core errors return
// "Error: ..." text (mapped to Success=false), and every output is bounded.
// Write-capable tools and approval-path integration are future work; the
// runtime's approval row keeps its `policy: ask` default.

const CORE_PROJECT_PROCEDURE = '/workos.project.v1.ProjectService/GetProject';
const CORE_ARTIFACTS_PROCEDURE = '/workos.artifact.v1.ArtifactService/ListArtifacts';
const MAX_OUTPUT_BYTES = 4096;
const TRUNCATION_MARKER = '\n[truncated]';
const FETCH_TIMEOUT_MS = 10000;
const MAX_ARTIFACTS = 50;

function sessionFacts() {
  return {
    coreURL: process.env.WORKOS_TOOL_CORE_URL || '',
    ownerID: process.env.WORKOS_TOOL_OWNER_ID || '',
    projectID: process.env.WORKOS_TOOL_PROJECT_ID || '',
    deviceID: process.env.WORKOS_TOOL_DEVICE_ID || '',
  };
}

// callCore performs one Connect unary JSON RPC against Core. The request
// body is the protojson encoding of the request message; the response body
// is the protojson encoding of the response message. Only sanitized error
// codes/messages surface — never raw bodies or credentials.
async function callCore(procedure, message, facts) {
  const response = await fetch(facts.coreURL + procedure, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
      'X-WorkOS-User-ID': facts.ownerID,
      'X-WorkOS-Device-ID': facts.deviceID,
    },
    body: JSON.stringify(message),
    signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
  });
  const text = await response.text();
  if (!response.ok) {
    let detail = 'HTTP ' + response.status;
    try {
      const parsed = JSON.parse(text);
      if (parsed && typeof parsed.message === 'string' && parsed.message.length > 0) {
        detail += ': ' + parsed.message.slice(0, 200);
      }
    } catch {}
    throw new Error('Core call failed: ' + detail);
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new Error('Core response is not valid JSON');
  }
}

function bound(text) {
  if (text.length <= MAX_OUTPUT_BYTES) {
    return text;
  }
  return text.slice(0, MAX_OUTPUT_BYTES - TRUNCATION_MARKER.length) + TRUNCATION_MARKER;
}

function errorMessage(error) {
  const raw = error && error.message ? String(error.message) : String(error);
  return 'Error: workos tool failed (' + raw.slice(0, 300) + ')';
}

async function projectInfo() {
  const facts = sessionFacts();
  if (!facts.coreURL || !facts.ownerID || !facts.projectID || !facts.deviceID) {
    throw new Error('WorkOS tool environment is not configured for this session');
  }
  const response = await callCore(CORE_PROJECT_PROCEDURE, { projectId: facts.projectID }, facts);
  const project = response.project || {};
  const binding = project.harnessBinding || {};
  const info = {
    projectId: project.id || '',
    name: project.name || '',
    harnessProviderId: binding.providerId || '',
    harnessInstancePolicy: binding.instancePolicy || '',
    workspaceRefs: (project.workspaceRefs || []).slice(0, 5).map((ref) => ({
      kind: ref.kind || '', uri: ref.uri || '', readOnly: !!ref.readOnly,
    })),
    revision: project.revision || 0,
  };
  return bound(JSON.stringify(info));
}

async function listArtifacts() {
  const facts = sessionFacts();
  if (!facts.coreURL || !facts.ownerID || !facts.projectID || !facts.deviceID) {
    throw new Error('WorkOS tool environment is not configured for this session');
  }
  const response = await callCore(
    CORE_ARTIFACTS_PROCEDURE,
    { projectId: facts.projectID, page: { pageSize: MAX_ARTIFACTS } },
    facts,
  );
  const artifacts = (response.artifacts || []).slice(0, MAX_ARTIFACTS).map((artifact) => ({
    id: artifact.id || '',
    type: artifact.type || '',
    title: artifact.title || '',
  }));
  return bound(JSON.stringify({ artifacts }));
}

export const name = 'workos-tools';
export const inject = ['tools'];

export function apply(ctx) {
  const textTool = (definition, run) =>
    ctx.tools.register({
      ...definition,
      timeoutMs: FETCH_TIMEOUT_MS + 5000,
      output: {
        schema: { type: 'string' },
        render: (_args, value) => [{ type: 'text', text: String(value) }],
      },
      execute() {
        // The result is a plain string on success and an "Error: ..." string
        // on failure; the session mapper marks the latter Success=false.
        return run().catch(errorMessage);
      },
    });

  textTool(
    {
      name: 'workos_project_info',
      description:
        'Report the WorkOS project this session is bound to: project id, name, ' +
        'active harness binding provider, workspace refs, and revision. ' +
        'Takes no parameters; the facts come from the session context.',
      parameters: { type: 'object', properties: {}, additionalProperties: false },
    },
    projectInfo,
  );

  textTool(
    {
      name: 'workos_list_artifacts',
      description:
        'List up to 50 artifacts (id, type, title) of the WorkOS project this ' +
        'session is bound to. Takes no parameters; the project comes from the ' +
        'session context.',
      parameters: { type: 'object', properties: {}, additionalProperties: false },
    },
    listArtifacts,
  );
}
