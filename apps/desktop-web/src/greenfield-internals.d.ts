// The pinned Greenfield release ships declarations under types/ while its
// browser I/O object lives under dist/. Keep this private import typed locally
// until the upstream package exports it.
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
