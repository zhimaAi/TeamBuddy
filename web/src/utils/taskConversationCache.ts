import type { TaskNotification, TaskProgress } from '@/types/pipeline'
import type { TaskWithDetails } from '@/types/task-detail'
import { t } from '@/i18n'
import { getActivePinia } from 'pinia'
import { useAuthStore } from '@/stores/auth'

const DATABASE_NAME = 'teamsboard-conversations'
const DATABASE_VERSION = 1
const CACHE_SCHEMA_VERSION = 1
const NOTIFICATION_STORE = 'notificationSnapshots'
const TASK_VIEW_STORE = 'taskViews'
const VIEW_STATE_STORE = 'viewStates'
const NOTIFICATION_SNAPSHOT_KEY = 'unarchived'
const VIEW_STATE_KEY = 'task-notifications'

interface NotificationSnapshotRecord {
  key: typeof NOTIFICATION_SNAPSHOT_KEY
  schemaVersion: number
  updatedAt: number
  items: TaskNotification[]
}

interface TaskViewRecord {
  taskUuid: string
  schemaVersion: number
  updatedAt: number
  task: TaskWithDetails
  progress: TaskProgress[]
}

interface ViewStateRecord {
  key: typeof VIEW_STATE_KEY
  schemaVersion: number
  updatedAt: number
  lastSelectedTaskUuid: string
  pipelineExpanded?: boolean
}

export interface PersistentTaskConversationCache {
  notifications: TaskNotification[]
  taskViews: Array<{
    taskUuid: string
    task: TaskWithDetails
    progress: TaskProgress[]
  }>
  lastSelectedTaskUuid: string
  pipelineExpanded: boolean
}

const databasePromises = new Map<string, Promise<IDBDatabase>>()

export function taskConversationCacheScope(): string {
  return getActivePinia() ? useAuthStore().cloudUser?.accountScope || 'local' : 'local'
}

export function releaseTaskConversationCaches() {
  for (const promise of databasePromises.values()) void promise.then(database => database.close()).catch(() => {})
  databasePromises.clear()
}

function requestResult<T>(request: IDBRequest<T>) {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error || new Error(t('components.errors.indexedDbRequest')))
  })
}

function transactionCompleted(transaction: IDBTransaction) {
  return new Promise<void>((resolve, reject) => {
    transaction.oncomplete = () => resolve()
    transaction.onabort = () => reject(transaction.error || new Error(t('components.errors.indexedDbAborted')))
    transaction.onerror = () => reject(transaction.error || new Error(t('components.errors.indexedDbTransaction')))
  })
}

function openDatabase(scope: string) {
  const existing = databasePromises.get(scope)
  if (existing) return existing
  const databasePromise = new Promise<IDBDatabase>((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error(t('components.errors.indexedDbUnsupported')))
      return
    }
    const request = indexedDB.open(`${DATABASE_NAME}-scoped-${encodeURIComponent(scope)}`, DATABASE_VERSION)
    request.onupgradeneeded = () => {
      const database = request.result
      if (!database.objectStoreNames.contains(NOTIFICATION_STORE)) {
        database.createObjectStore(NOTIFICATION_STORE, { keyPath: 'key' })
      }
      if (!database.objectStoreNames.contains(TASK_VIEW_STORE)) {
        database.createObjectStore(TASK_VIEW_STORE, { keyPath: 'taskUuid' })
      }
      if (!database.objectStoreNames.contains(VIEW_STATE_STORE)) {
        database.createObjectStore(VIEW_STATE_STORE, { keyPath: 'key' })
      }
    }
    request.onsuccess = () => {
      const database = request.result
      database.onversionchange = () => {
        database.close()
        if (databasePromises.get(scope) === databasePromise) databasePromises.delete(scope)
      }
      resolve(database)
    }
    request.onerror = () => {
      if (databasePromises.get(scope) === databasePromise) databasePromises.delete(scope)
      reject(request.error || new Error(t('components.errors.indexedDbOpen')))
    }
    request.onblocked = () => {
      if (databasePromises.get(scope) === databasePromise) databasePromises.delete(scope)
      reject(new Error(t('components.errors.indexedDbBlocked')))
    }
  })
  databasePromises.set(scope, databasePromise)
  return databasePromise
}

export async function readTaskConversationCache(scope = taskConversationCacheScope()): Promise<PersistentTaskConversationCache> {
  const database = await openDatabase(scope)
  const transaction = database.transaction(
    [NOTIFICATION_STORE, TASK_VIEW_STORE, VIEW_STATE_STORE],
    'readonly',
  )
  const completed = transactionCompleted(transaction)
  const notificationRequest = transaction
    .objectStore(NOTIFICATION_STORE)
    .get(NOTIFICATION_SNAPSHOT_KEY) as IDBRequest<NotificationSnapshotRecord | undefined>
  const taskViewsRequest = transaction
    .objectStore(TASK_VIEW_STORE)
    .getAll() as IDBRequest<TaskViewRecord[]>
  const viewStateRequest = transaction
    .objectStore(VIEW_STATE_STORE)
    .get(VIEW_STATE_KEY) as IDBRequest<ViewStateRecord | undefined>
  const [notificationSnapshot, taskViewRecords, viewState] = await Promise.all([
    requestResult(notificationRequest),
    requestResult(taskViewsRequest),
    requestResult(viewStateRequest),
    completed,
  ])

  return {
    notifications:
      notificationSnapshot?.schemaVersion === CACHE_SCHEMA_VERSION &&
      Array.isArray(notificationSnapshot.items)
        ? notificationSnapshot.items
        : [],
    taskViews: taskViewRecords
      .filter((record) => record.schemaVersion === CACHE_SCHEMA_VERSION)
      .map((record) => ({
        taskUuid: record.taskUuid,
        task: record.task,
        progress: record.progress,
      })),
    lastSelectedTaskUuid:
      viewState?.schemaVersion === CACHE_SCHEMA_VERSION
        ? viewState.lastSelectedTaskUuid
        : '',
    pipelineExpanded:
      viewState?.schemaVersion === CACHE_SCHEMA_VERSION &&
      typeof viewState.pipelineExpanded === 'boolean'
        ? viewState.pipelineExpanded
        : true,
  }
}

export async function saveTaskNotificationSnapshot(items: TaskNotification[], scope = taskConversationCacheScope()) {
  const database = await openDatabase(scope)
  const transaction = database.transaction(NOTIFICATION_STORE, 'readwrite')
  transaction.objectStore(NOTIFICATION_STORE).put({
    key: NOTIFICATION_SNAPSHOT_KEY,
    schemaVersion: CACHE_SCHEMA_VERSION,
    updatedAt: Date.now(),
    items,
  } satisfies NotificationSnapshotRecord)
  await transactionCompleted(transaction)
}

export async function saveTaskView(
  taskUuid: string,
  task: TaskWithDetails,
  progress: TaskProgress[],
  scope = taskConversationCacheScope(),
) {
  const database = await openDatabase(scope)
  const transaction = database.transaction(TASK_VIEW_STORE, 'readwrite')
  transaction.objectStore(TASK_VIEW_STORE).put({
    taskUuid,
    schemaVersion: CACHE_SCHEMA_VERSION,
    updatedAt: Date.now(),
    task,
    progress,
  } satisfies TaskViewRecord)
  await transactionCompleted(transaction)
}

export async function saveTaskConversationViewState(
  taskUuid: string,
  pipelineExpanded: boolean,
  scope = taskConversationCacheScope(),
) {
  const database = await openDatabase(scope)
  const transaction = database.transaction(VIEW_STATE_STORE, 'readwrite')
  transaction.objectStore(VIEW_STATE_STORE).put({
    key: VIEW_STATE_KEY,
    schemaVersion: CACHE_SCHEMA_VERSION,
    updatedAt: Date.now(),
    lastSelectedTaskUuid: taskUuid,
    pipelineExpanded,
  } satisfies ViewStateRecord)
  await transactionCompleted(transaction)
}

export async function deleteTaskView(taskUuid: string, scope = taskConversationCacheScope()) {
  const database = await openDatabase(scope)
  const transaction = database.transaction(TASK_VIEW_STORE, 'readwrite')
  transaction.objectStore(TASK_VIEW_STORE).delete(taskUuid)
  await transactionCompleted(transaction)
}

export async function removeDeletedTaskFromCache(taskUuid: string, scope = taskConversationCacheScope()) {
  const database = await openDatabase(scope)
  const transaction = database.transaction(
    [NOTIFICATION_STORE, TASK_VIEW_STORE, VIEW_STATE_STORE], 'readwrite',
  )
  const completed = transactionCompleted(transaction)
  transaction.objectStore(TASK_VIEW_STORE).delete(taskUuid)
  const notifications = transaction.objectStore(NOTIFICATION_STORE)
  const snapshot = notifications.get(NOTIFICATION_SNAPSHOT_KEY) as IDBRequest<NotificationSnapshotRecord | undefined>
  snapshot.onsuccess = () => {
    if (snapshot.result) {
      notifications.put({
        ...snapshot.result,
        items: snapshot.result.items.filter((item) => item.task_uuid !== taskUuid),
      })
    }
  }
  const viewStates = transaction.objectStore(VIEW_STATE_STORE)
  const viewState = viewStates.get(VIEW_STATE_KEY) as IDBRequest<ViewStateRecord | undefined>
  viewState.onsuccess = () => {
    if (viewState.result?.lastSelectedTaskUuid === taskUuid) {
      viewStates.put({ ...viewState.result, lastSelectedTaskUuid: '' })
    }
  }
  await completed
}

export async function deleteTaskViewsOutside(validTaskUuids: Set<string>, scope = taskConversationCacheScope()) {
  const database = await openDatabase(scope)
  const transaction = database.transaction(TASK_VIEW_STORE, 'readwrite')
  const store = transaction.objectStore(TASK_VIEW_STORE)
  const cursorRequest = store.openCursor()
  cursorRequest.onsuccess = () => {
    const cursor = cursorRequest.result
    if (!cursor) return
    const record = cursor.value as TaskViewRecord
    if (
      record.schemaVersion !== CACHE_SCHEMA_VERSION ||
      !validTaskUuids.has(record.taskUuid)
    ) {
      cursor.delete()
    }
    cursor.continue()
  }
  await transactionCompleted(transaction)
}
