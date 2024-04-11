import { openDB } from "idb"

const storeName = "events.2"

export async function createEventStore() {
  const eventStore = await openDB("swa.sh", 3, {
    upgrade: (db, oldVersion, newVersion, transaction) => {
      db.createObjectStore(storeName, {
        keyPath: "sequenceNumber",
        autoIncrement: true,
      })
    },
  })

  return eventStore
}

export async function saveEvent(eventStore, payload) {
  const key = await eventStore.add(storeName, {
    timestamp: Date.now(),
    payload,
  })

  return await eventStore.get(storeName, key)
}

export async function getAllEvents(eventStore) {
  return await eventStore.getAll(storeName)
}
