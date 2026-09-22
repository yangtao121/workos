// Only this gate's names resolve; no forwarding and no production discovery.
import dgram from "node:dgram";
const server = dgram.createSocket("udp4");
const gateway = (process.env.WORKOS_ANDROID_GATEWAY ?? "").split(".").map(Number);
if (gateway.length !== 4 || gateway.some((v) => !Number.isInteger(v) || v < 0 || v > 255))
  throw new Error("fixture gateway required");
server.on("message", (query, remote) => {
  if (query.length < 17 || query.readUInt16BE(4) !== 1) return;
  let offset = 12;
  const labels = [];
  while (offset < query.length && query[offset]) {
    const length = query[offset++];
    if (length > 63 || offset + length > query.length) return;
    labels.push(query.subarray(offset, offset + length).toString("ascii"));
    offset += length;
  }
  offset++;
  if (offset + 4 > query.length) return;
  const known = ["workos.fixture", "wrong.workos.fixture"].includes(labels.join(".").toLowerCase());
  const hasAnswer =
    known && query.readUInt16BE(offset) === 1 && query.readUInt16BE(offset + 2) === 1;
  const header = Buffer.alloc(12);
  query.copy(header, 0, 0, 2);
  header.writeUInt16BE(known ? 0x8180 : 0x8183, 2);
  header.writeUInt16BE(1, 4);
  header.writeUInt16BE(hasAnswer ? 1 : 0, 6);
  const answer = hasAnswer
    ? Buffer.from([0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, ...gateway])
    : Buffer.alloc(0);
  server.send(
    Buffer.concat([header, query.subarray(12, offset + 4), answer]),
    remote.port,
    remote.address,
  );
});
server.bind(53, "0.0.0.0");
