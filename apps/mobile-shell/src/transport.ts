// Native webviews are not the deployment's browser origin. Use the bundled
// native HTTP bridge (and its native cookie jar) for unary Connect JSON RPCs.
// Gateway Origin/Host checks and TLS certificate verification remain required.
import { Capacitor, CapacitorHttp } from "@capacitor/core";
import { createConnectTransport } from "@connectrpc/connect-web";

export function createMobileTransport(origin: string) {
  const canonical = new URL(origin).origin;
  return createConnectTransport({
    baseUrl: canonical,
    useBinaryFormat: false,
    fetch: async (input, init) => {
      if (!Capacitor.isNativePlatform()) {
        return globalThis.fetch(input, { ...init, credentials: "include" });
      }
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (
        url.protocol !== "https:" ||
        url.origin !== canonical ||
        request.method !== "POST" ||
        !url.pathname.startsWith("/workos.") ||
        request.headers.get("Content-Type") !== "application/json"
      ) {
        throw new Error("native transport only accepts deployment unary JSON RPCs");
      }
      request.signal.throwIfAborted();
      const headers = Object.fromEntries(request.headers.entries());
      headers.Origin = canonical;
      const response = await CapacitorHttp.request({
        url: request.url,
        method: "POST",
        headers,
        data: JSON.parse(await request.text()) as unknown,
        responseType: "text",
        connectTimeout: 10000,
        readTimeout: 30000,
        disableRedirects: true,
      });
      request.signal.throwIfAborted();
      if (response.status >= 300 && response.status < 400)
        throw new Error("gateway redirect refused");
      const data: unknown = response.data;
      const responseHeaders = new Headers(response.headers);
      // The native bridge already decodes content; do not advertise compression.
      responseHeaders.delete("content-encoding");
      responseHeaders.delete("content-length");
      return new Response(typeof data === "string" ? data : JSON.stringify(data), {
        status: response.status,
        headers: responseHeaders,
      });
    },
  });
}
