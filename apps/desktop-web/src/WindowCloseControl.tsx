import { Icon } from "@workos/ui-kit";

export function WindowCloseControl({
  title,
  pending = false,
  onClose,
}: {
  title: string;
  pending?: boolean;
  onClose: () => void;
}) {
  return (
    <>
      {pending ? <span className="window-closing-status">正在关闭…</span> : null}
      <button
        type="button"
        className="window-close"
        aria-label={`Close ${title}`}
        disabled={pending}
        title={pending ? "Waiting for the native app to close this dialog" : undefined}
        onClick={onClose}
      >
        <Icon name="close" size={16} />
      </button>
    </>
  );
}
