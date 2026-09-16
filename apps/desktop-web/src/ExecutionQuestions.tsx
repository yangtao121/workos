import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { ExecutionInteraction } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

export function ExecutionQuestions({
  taskId,
  workosClients,
}: {
  taskId: string;
  workosClients: WorkOSClients;
}) {
  const [questions, setQuestions] = useState<ExecutionInteraction[]>([]);
  const [error, setError] = useState<string>();
  useEffect(() => {
    let active = true;
    const load = async () => {
      try {
        const result = await workosClients.agentInteractions.listTaskInteractions({ taskId });
        if (active) {
          setQuestions(result.interactions);
          setError(undefined);
        }
      } catch {
        if (active) setError("Questions could not be refreshed. No answer has been submitted.");
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), 1500);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [taskId, workosClients]);
  return (
    <div className="execution-questions" aria-label="Execution questions">
      {error ? <p role="status">{error}</p> : null}
      {questions.map((question) => (
        <QuestionCard key={question.id} interaction={question} workosClients={workosClients} />
      ))}
    </div>
  );
}
function QuestionCard({
  interaction,
  workosClients,
}: {
  interaction: ExecutionInteraction;
  workosClients: WorkOSClients;
}) {
  const [answers, setAnswers] = useState<Record<string, { selected: string[]; text: string }>>({});
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<string>();
  const [error, setError] = useState<string>();
  const intent = useRef<{ signature: string; key: string } | undefined>(undefined);
  const state = interaction.state !== "pending" ? interaction.state : (result ?? interaction.state);
  const respond = async (reject: boolean) => {
    if (busy || state !== "pending") return;
    const payload = reject
      ? []
      : interaction.questions.map((question) => ({
          questionId: question.id,
          selected: answers[question.id]?.selected ?? [],
          text: answers[question.id]?.text ?? "",
        }));
    const signature = JSON.stringify([reject, payload]);
    if (intent.current?.signature !== signature)
      intent.current = { signature, key: crypto.randomUUID() };
    setBusy(true);
    setError(undefined);
    try {
      const response = await workosClients.agentInteractions.respondExecutionInteraction({
        interactionId: interaction.id,
        idempotencyKey: intent.current.key,
        reject,
        answers: payload,
      });
      setResult(response.interaction?.state ?? "answered");
    } catch {
      setError(
        "The answer could not be confirmed. This question may have expired or the execution may have stopped; retry only the same answer.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="execution-question"
      onSubmit={(event) => {
        event.preventDefault();
        void respond(false);
      }}
    >
      <h3>{state === "pending" ? "Agent needs your answer" : `Question ${state}`}</h3>
      <p>Answers apply only to this execution. Pending questions expire after two minutes.</p>
      {interaction.questions.map((question) => (
        <fieldset key={question.id} disabled={busy || state !== "pending"}>
          <legend>{question.text}</legend>
          {question.detail ? <pre>{question.detail}</pre> : null}
          {question.choices.map((choice) => (
            <label className="execution-question-choice" key={choice.label}>
              <input
                type={question.multiple ? "checkbox" : "radio"}
                name={`${interaction.id}-${question.id}`}
                checked={answers[question.id]?.selected.includes(choice.label) ?? false}
                onChange={(event) => {
                  setAnswers((current) => {
                    const old = current[question.id] ?? { selected: [], text: "" };
                    const selected = question.multiple
                      ? event.target.checked
                        ? [...old.selected, choice.label]
                        : old.selected.filter((value) => value !== choice.label)
                      : [choice.label];
                    return { ...current, [question.id]: { ...old, selected } };
                  });
                }}
              />
              <span>
                {choice.label}
                {choice.description ? ` — ${choice.description}` : ""}
              </span>
            </label>
          ))}
          <label>
            Your answer
            <textarea
              maxLength={8192}
              value={answers[question.id]?.text ?? ""}
              onChange={(event) => {
                setAnswers((current) => ({
                  ...current,
                  [question.id]: {
                    selected: current[question.id]?.selected ?? [],
                    text: event.target.value,
                  },
                }));
              }}
            />
          </label>
        </fieldset>
      ))}
      {error ? <p role="alert">{error}</p> : null}
      {state === "pending" ? (
        <div className="preview-actions">
          <Button
            type="submit"
            disabled={
              busy ||
              interaction.questions.some(
                (question) =>
                  !answers[question.id]?.text.trim() && !answers[question.id]?.selected.length,
              )
            }
          >
            Send answer
          </Button>
          <Button type="button" disabled={busy} onClick={() => void respond(true)}>
            Reject question
          </Button>
        </div>
      ) : null}
    </form>
  );
}
