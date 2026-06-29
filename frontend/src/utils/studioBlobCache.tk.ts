/**
 * TokenKey-only: browser-local blob cache for Studio generated media.
 *
 * localStorage metadata (prompt, cost, model) stays small; inline data: / blob:
 * payloads live in IndexedDB so a refresh can still show thumbnails and replay
 * video without TokenKey hosting media (#944 pass-through). Best-effort: quota
 * errors trim oldest entries; TTL evicts stale blobs.
 */
const DB_NAME = 'tk_studio_blob_v1'
const STORE = 'blobs'
/** Keep blobs ~7 days — enough for a work session + return visit, not a CDN. */
const TTL_MS = 7 * 24 * 60 * 60 * 1000
/** Per-user soft cap (decoded bytes). */
const MAX_BYTES_PER_USER = 150 * 1024 * 1024

export type StudioBlobKind = 'image' | 'video'

interface BlobRecord {
  /** `${userId}:${kind}:${itemId}` */
  key: string
  userId: string
  kind: StudioBlobKind
  itemId: string
  mime: string
  bytes: number
  ts: number
}

function recordKey(userId: string | number, kind: StudioBlobKind, itemId: string): string {
  return `${userId}:${kind}:${itemId}`
}

function openDb(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error('indexedDB_unavailable'))
      return
    }
    const req = indexedDB.open(DB_NAME, 1)
    req.onerror = () => reject(req.error ?? new Error('idb_open_failed'))
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) {
        const store = db.createObjectStore(STORE, { keyPath: 'key' })
        store.createIndex('by_user', 'userId', { unique: false })
      }
    }
    req.onsuccess = () => resolve(req.result)
  })
}

function tx<T>(
  db: IDBDatabase,
  mode: IDBTransactionMode,
  fn: (store: IDBObjectStore) => IDBRequest<T> | void
): Promise<T | void> {
  return new Promise((resolve, reject) => {
    const t = db.transaction(STORE, mode)
    const store = t.objectStore(STORE)
    const req = fn(store)
    t.oncomplete = () => resolve(req ? (req as IDBRequest<T>).result : undefined)
    t.onerror = () => reject(t.error ?? new Error('idb_tx_failed'))
  })
}

function dataUriToBlob(src: string): Blob | null {
  const m = /^data:([\w.+-]+\/[\w.+-]+);base64,([A-Za-z0-9+/=]+)$/i.exec(src)
  if (!m || typeof atob !== 'function') return null
  try {
    const binary = atob(m[2])
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    return new Blob([bytes], { type: m[1] })
  } catch {
    return null
  }
}

async function enforceQuota(db: IDBDatabase, userId: string, incomingBytes: number): Promise<void> {
  const index = db.transaction(STORE, 'readonly').objectStore(STORE).index('by_user')
  const rows: BlobRecord[] = await new Promise((resolve, reject) => {
    const out: BlobRecord[] = []
    const req = index.openCursor(IDBKeyRange.only(userId))
    req.onerror = () => reject(req.error)
    req.onsuccess = () => {
      const cur = req.result
      if (!cur) {
        resolve(out)
        return
      }
      out.push(cur.value as BlobRecord)
      cur.continue()
    }
  })
  rows.sort((a, b) => a.ts - b.ts)
  let total = rows.reduce((s, r) => s + (r.bytes || 0), 0) + incomingBytes
  const toDrop: string[] = []
  for (const row of rows) {
    if (total <= MAX_BYTES_PER_USER) break
    toDrop.push(row.key)
    total -= row.bytes || 0
  }
  if (!toDrop.length) return
  await tx(db, 'readwrite', (store) => {
    for (const k of toDrop) store.delete(k)
  })
}

/**
 * Persist media bytes from an in-tab src (data: URI or blob:).
 */
export async function cacheStudioBlobFromSrc(
  userId: string | number,
  kind: StudioBlobKind,
  itemId: string,
  src: string
): Promise<boolean> {
  if (!src || (!/^data:/i.test(src) && !src.startsWith('blob:'))) return false
  let blob: Blob | null = null
  if (/^data:/i.test(src)) blob = dataUriToBlob(src)
  else if (src.startsWith('blob:') && typeof fetch === 'function') {
    try {
      const res = await fetch(src)
      blob = await res.blob()
    } catch {
      return false
    }
  }
  if (!blob || blob.size === 0) return false
  return putBlobRecord(userId, kind, itemId, blob)
}

/** Fetch an upstream http(s) clip when CORS allows and mirror into IndexedDB. */
export async function cacheStudioBlobFromHttpUrl(
  userId: string | number,
  kind: StudioBlobKind,
  itemId: string,
  url: string
): Promise<boolean> {
  if (!url || !/^https?:\/\//i.test(url) || typeof fetch !== 'function') return false
  try {
    const res = await fetch(url, { mode: 'cors' })
    if (!res.ok) return false
    const blob = await res.blob()
    if (!blob.size) return false
    return putBlobRecord(userId, kind, itemId, blob)
  } catch {
    return false
  }
}

async function putBlobRecord(
  userId: string | number,
  kind: StudioBlobKind,
  itemId: string,
  blob: Blob
): Promise<boolean> {
  try {
    const db = await openDb()
    await enforceQuota(db, String(userId), blob.size)
    const rec: BlobRecord & { blob: Blob } = {
      key: recordKey(userId, kind, itemId),
      userId: String(userId),
      kind,
      itemId,
      mime: blob.type || (kind === 'video' ? 'video/mp4' : 'image/png'),
      bytes: blob.size,
      ts: Date.now(),
      blob,
    }
    await tx(db, 'readwrite', (store) => store.put(rec))
    db.close()
    return true
  } catch {
    return false
  }
}

/** Mint a tab-local object URL for a cached blob, or '' when missing/expired. */
export async function getStudioBlobObjectUrl(
  userId: string | number,
  kind: StudioBlobKind,
  itemId: string
): Promise<string> {
  if (typeof URL?.createObjectURL !== 'function') return ''
  try {
    const db = await openDb()
    const key = recordKey(userId, kind, itemId)
    const row = (await tx(db, 'readonly', (store) => store.get(key))) as
      | (BlobRecord & { blob?: Blob })
      | undefined
    db.close()
    if (!row?.blob) return ''
    if (Date.now() - row.ts > TTL_MS) {
      void deleteStudioBlob(userId, kind, itemId)
      return ''
    }
    return URL.createObjectURL(row.blob)
  } catch {
    return ''
  }
}

export async function deleteStudioBlob(
  userId: string | number,
  kind: StudioBlobKind,
  itemId: string
): Promise<void> {
  try {
    const db = await openDb()
    await tx(db, 'readwrite', (store) => store.delete(recordKey(userId, kind, itemId)))
    db.close()
  } catch {
    /* best-effort */
  }
}

/** Drop expired blobs for one user (call on Studio mount). */
export async function pruneStudioBlobCache(userId: string | number): Promise<void> {
  try {
    const db = await openDb()
    const uid = String(userId)
    const index = db.transaction(STORE, 'readonly').objectStore(STORE).index('by_user')
    const stale: string[] = await new Promise((resolve, reject) => {
      const keys: string[] = []
      const req = index.openCursor(IDBKeyRange.only(uid))
      req.onerror = () => reject(req.error)
      req.onsuccess = () => {
        const cur = req.result
        if (!cur) {
          resolve(keys)
          return
        }
        const row = cur.value as BlobRecord
        if (Date.now() - row.ts > TTL_MS) keys.push(row.key)
        cur.continue()
      }
    })
    if (stale.length) {
      await tx(db, 'readwrite', (store) => {
        for (const k of stale) store.delete(k)
      })
    }
    db.close()
  } catch {
    /* best-effort */
  }
}
