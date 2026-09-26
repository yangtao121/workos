declare module "@gfld/compositor/dist/web/WebInputOutput.js" {
  export const webInputOutput: {
    mkfifo(): Promise<
      [
        { readBlob(): Promise<Blob>; close(): Promise<void> },
        { write(data: Blob): Promise<void>; close(): Promise<void> },
      ]
    >;
  };
}
