// IndexedDB persistence for the browser profile device credential. The
// CryptoKey is structured-cloneable; the private key stays non-extractable
// through the round trip. Any storage failure surfaces as an error so the
// pairing flow stops before claiming a ticket. Writers only report success
// after the transaction has committed (durability), never on request
// success alone.

const DATABASE_NAME = "workos-device-auth";
const DATABASE_VERSION = 1;
const STORE_NAME = "device";
const RECORD_ID = "profile";

export interface StoredDeviceIdentity {
  privateKey: CryptoKey;
  publicKeyHash: string;
  // publicKeySpki is the canonical SPKI DER — public material, needed to
  // submit with the pairing proof after a page reload.
  publicKeySpki: Uint8Array;
  // deviceId is the pending or paired server-minted device id.
  deviceId?: string;
  deviceName?: string;
  deviceClass?: string;
}

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (typeof indexedDB === "undefined") {
      reject(new Error("IndexedDB is unavailable in this browser context"));
      return;
    }
    const request = indexedDB.open(DATABASE_NAME, DATABASE_VERSION);
    request.onupgradeneeded = () => {
      const database = request.result;
      if (!database.objectStoreNames.contains(STORE_NAME)) {
        database.createObjectStore(STORE_NAME, { keyPath: "id" });
      }
    };
    request.onsuccess = () => {
      resolve(request.result);
    };
    request.onerror = () => {
      reject(request.error ?? new Error("IndexedDB open failed"));
    };
  });
}

// withStore opens one transaction and resolves only after the transaction
// has committed, so a save is durable before the pairing flow continues;
// any request or transaction error rejects.
async function withStore<T>(
  mode: IDBTransactionMode,
  run: (store: IDBObjectStore) => IDBRequest<T>,
): Promise<T> {
  const database = await openDatabase();
  try {
    return await new Promise<T>((resolve, reject) => {
      const transaction = database.transaction(STORE_NAME, mode);
      let requestResult: T;
      transaction.oncomplete = () => {
        resolve(requestResult);
      };
      transaction.onabort = () => {
        reject(transaction.error ?? new Error("IndexedDB transaction aborted"));
      };
      const request = run(transaction.objectStore(STORE_NAME));
      request.onsuccess = () => {
        requestResult = request.result;
      };
      request.onerror = () => {
        reject(request.error ?? new Error("IndexedDB request failed"));
      };
    });
  } finally {
    database.close();
  }
}

export async function loadDeviceIdentity(): Promise<StoredDeviceIdentity | undefined> {
  const record = await withStore<StoredDeviceIdentity | undefined>(
    "readonly",
    (store) => store.get(RECORD_ID) as IDBRequest<StoredDeviceIdentity | undefined>,
  );
  return record ?? undefined;
}

export async function saveDeviceIdentity(identity: StoredDeviceIdentity): Promise<void> {
  await withStore("readwrite", (store) => store.put({ ...identity, id: RECORD_ID }));
}

export async function clearDeviceIdentity(): Promise<void> {
  await withStore("readwrite", (store) => store.delete(RECORD_ID));
}
