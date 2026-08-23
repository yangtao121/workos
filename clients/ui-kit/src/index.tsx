import type { ButtonHTMLAttributes, PropsWithChildren } from "react";

export function Button({
  children,
  ...props
}: PropsWithChildren<ButtonHTMLAttributes<HTMLButtonElement>>) {
  return (
    <button className="workos-button" {...props}>
      {children}
    </button>
  );
}
