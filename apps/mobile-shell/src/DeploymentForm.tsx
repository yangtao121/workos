import { useState } from "react";
import { parseDeployment } from "./deployment.js";

export function DeploymentForm(props: {
  origin?: string;
  onConnect: (origin: string, fragment: string | undefined) => void;
}) {
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  return (
    <form
      className="mobile-deployment-form"
      onSubmit={(event) => {
        event.preventDefault();
        // Clear even invalid input: a malformed link can still contain a ticket.
        setValue("");
        try {
          const parsed = parseDeployment(value);
          if (props.origin && (parsed.origin !== props.origin || !parsed.fragment))
            throw new Error("pairing link does not match this server");
          setError("");
          props.onConnect(parsed.origin, parsed.fragment);
        } catch {
          setError(
            props.origin
              ? "Enter a valid pairing link for this server."
              : "Enter a valid HTTPS WorkOS address or pairing link.",
          );
        }
      }}
    >
      <label>
        {props.origin ? "Pairing link" : "WorkOS address or pairing link"}
        <input
          value={value}
          onChange={(event) => {
            setValue(event.target.value);
          }}
          autoComplete="off"
          spellCheck={false}
          autoCapitalize="none"
          inputMode="url"
          type="password"
          required
        />
      </label>
      <button type="submit" className="mobile-button">
        {props.origin ? "Pair device" : "Connect"}
      </button>
      {error ? <p role="alert">{error}</p> : null}
    </form>
  );
}
