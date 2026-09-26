import { create, type MessageInitShape } from "@bufbuild/protobuf";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldInputVerdict, GreenfieldWindowInputEventSchema } from "@workos/protocol";

export const GREENFIELD_CLIPBOARD_LIMIT = 1024 * 1024;
export const GREENFIELD_TEXT_COMMIT_LIMIT = 16 * 1024;

export interface GreenfieldAttachment {
  sessionId: string;
  attachmentId: string;
  workloadGeneration: bigint;
  controlGeneration: bigint;
  controls: boolean;
}

export type GreenfieldWindowEvent = NonNullable<
  MessageInitShape<typeof GreenfieldWindowInputEventSchema>["event"]
>;

// One sequence belongs to one live attachment, across every top-level window.
// Calls are serialized before sequence assignment so a discarded old-control
// intent cannot leave a gap that makes later input invalid.
export class GreenfieldWindowInputClient {
  private sequence = 0n;
  private epoch = 0;
  private tail: Promise<void> = Promise.resolve();
  private usable = true;

  constructor(
    private readonly client: WorkOSClients["greenfieldWindows"],
    private readonly attachment: GreenfieldAttachment,
  ) {}

  setControl(controls: boolean, controlGeneration: bigint) {
    if (
      this.attachment.controls !== controls ||
      this.attachment.controlGeneration !== controlGeneration
    ) {
      this.epoch++;
      this.attachment.controls = controls;
      this.attachment.controlGeneration = controlGeneration;
    }
  }

  get canControl() {
    return this.usable && this.attachment.controls;
  }

  send(windowId: string, inputs: GreenfieldWindowEvent[]): Promise<void> {
    if (!windowId || inputs.length < 1 || inputs.length > 64)
      return Promise.reject(new Error("invalid native input batch"));
    const epoch = this.epoch;
    const run = async () => {
      if (!this.canControl || epoch !== this.epoch) throw new Error("native control unavailable");
      const events = inputs.map((event, index) =>
        create(GreenfieldWindowInputEventSchema, {
          sequence: this.sequence + BigInt(index + 1),
          windowId,
          event,
        }),
      );
      const request = {
        sessionId: this.attachment.sessionId,
        attachmentId: this.attachment.attachmentId,
        expectedWorkloadGeneration: this.attachment.workloadGeneration,
        controlGeneration: this.attachment.controlGeneration,
        events,
      };
      let response;
      try {
        response = await this.client.sendGreenfieldWindowInput(request);
      } catch {
        // Retry the exact same sequence once. A fresh sequence could duplicate
        // a text or clipboard write whose first response was lost.
        if (epoch !== this.epoch) throw new Error("native control unavailable");
        try {
          response = await this.client.sendGreenfieldWindowInput(request);
        } catch {
          this.usable = false;
          throw new Error("native input result unknown; reconnect the viewer");
        }
      }
      if (epoch !== this.epoch) throw new Error("native control unavailable");
      if (
        response.verdict !== GreenfieldInputVerdict.APPLIED ||
        response.lastAppliedSequence !== events[events.length - 1]?.sequence
      ) {
        this.usable = false;
        throw new Error("native input was rejected");
      }
      this.sequence = response.lastAppliedSequence;
    };
    const result = this.tail.then(run);
    this.tail = result.catch(() => undefined);
    return result;
  }

  async readClipboard(): Promise<string> {
    if (!this.canControl) throw new Error("native control unavailable");
    const response = await this.client.readGreenfieldClipboard({
      sessionId: this.attachment.sessionId,
      attachmentId: this.attachment.attachmentId,
      expectedWorkloadGeneration: this.attachment.workloadGeneration,
      controlGeneration: this.attachment.controlGeneration,
    });
    if (response.textUtf8.byteLength > GREENFIELD_CLIPBOARD_LIMIT)
      throw new Error("native clipboard is too large");
    return new TextDecoder("utf-8", { fatal: true }).decode(response.textUtf8);
  }
}
