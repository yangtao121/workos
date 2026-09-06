import type { ButtonHTMLAttributes, PropsWithChildren } from "react";

export function Button({
  children,
  className,
  ...props
}: PropsWithChildren<ButtonHTMLAttributes<HTMLButtonElement>>) {
  return (
    <button {...props} className={["workos-button", className].filter(Boolean).join(" ")}>
      {children}
    </button>
  );
}

export { Icon, type IconName } from "./icons.js";
