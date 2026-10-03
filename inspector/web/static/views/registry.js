// View registry. The shell renders whatever view is registered for a
// snapshot.type, so adding an adapter means registering one view here or in
// its own module. The shell itself never learns about target systems.
//
// A view is { title, render(container, { snapshot, events, focus }) }.
//   snapshot: the latest Snapshot ({ type, source, seq, timestamp, data })
//   events:   events up to the playback cursor, oldest first
//   focus:    the event under the cursor, or null when following live
// render() is called again on every update, so it must be idempotent and
// must replace the container's contents rather than append to them.

const views = new Map();

export function registerView(type, view) {
  views.set(type, view);
}

export function viewFor(type) {
  return views.get(type) ?? null;
}
