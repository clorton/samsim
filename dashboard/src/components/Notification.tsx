import { createPortal } from "react-dom";
import type { ReactNode } from "react";

// All transient feedback is outside document flow: neither incoming telemetry
// nor dismissing a message can move the controls, map or history panel.
export default function Notification({
  children,
  error = false,
  onDismiss,
}: {
  children: ReactNode;
  error?: boolean;
  onDismiss?: () => void;
}) {
  const host = document.getElementById("notifications");
  if (!host) return null;
  return createPortal(
    <div
      className={`notification-card ${error ? "notification-error" : ""}`}
      role={error ? "alert" : "status"}
    >
      <div>{children}</div>
      {onDismiss && (
        <button aria-label="Dismiss notification" onClick={onDismiss}>
          ×
        </button>
      )}
    </div>,
    host,
  );
}
