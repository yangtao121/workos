// @vitest-environment node
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { expect, test, vi } from "vitest";

const source = readFileSync(new URL("../public/push-worker.js", import.meta.url), "utf8");
const first = "01999999-9999-7999-8999-000000000991";
const second = "01999999-9999-7999-8999-000000000992";

function worker(show: (tag: string) => Promise<void>) {
  type Wake = { data: { text(): string; json(): unknown }; waitUntil(work: Promise<void>): void };
  const handlers = new Map<string, (event: Wake) => void>();
  const shown = new Set<string>();
  const getNotifications = vi.fn(({ tag }: { tag: string }) =>
    Promise.resolve(shown.has(tag) ? [tag] : []),
  );
  const postMessage = vi.fn();
  const showNotification = vi.fn(async (_title: string, { tag }: { tag: string }) => {
    await show(tag);
    shown.add(tag);
  });
  runInNewContext(source, {
    self: {
      addEventListener: (name: string, handler: (event: Wake) => void) =>
        handlers.set(name, handler),
      registration: { getNotifications, showNotification },
      clients: { matchAll: () => Promise.resolve([{ postMessage }]) },
    },
  });
  return {
    getNotifications,
    showNotification,
    postMessage,
    push: (notificationId: string) => {
      let work: Promise<void> | undefined;
      handlers.get("push")?.({
        data: { text: () => JSON.stringify({ notificationId }), json: () => ({ notificationId }) },
        waitUntil: (pending) => {
          work = pending;
        },
      });
      if (!work) throw new Error("push was not retained by the worker");
      return work;
    },
  };
}

test("overlapping wakes finish display before deduplication of the next wake", async () => {
  let release: (() => void) | undefined;
  const display = new Promise<void>((resolve) => {
    release = resolve;
  });
  const receiver = worker(() => display);
  const pending = [receiver.push(first), receiver.push(first), receiver.push(second)];
  await vi.waitFor(() => {
    expect(receiver.showNotification).toHaveBeenCalledTimes(1);
  });
  expect(receiver.getNotifications).toHaveBeenCalledTimes(1);
  release?.();
  await Promise.all(pending);
  expect(receiver.showNotification).toHaveBeenCalledTimes(2);
  expect(receiver.postMessage).toHaveBeenCalledTimes(3);
});

test("a failed notification display does not discard later wakes", async () => {
  const receiver = worker((tag) =>
    tag === `workos-${first}`
      ? Promise.reject(new Error("display unavailable"))
      : Promise.resolve(),
  );
  const failed = expect(receiver.push(first)).rejects.toThrow("display unavailable");
  const later = receiver.push(second);
  await failed;
  await later;
  expect(receiver.showNotification).toHaveBeenCalledTimes(2);
  expect(receiver.postMessage).toHaveBeenCalledTimes(1);
});
