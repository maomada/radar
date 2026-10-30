import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { PrimaryNavRail } from "./PrimaryNavRail";

function renderRail(whatsNew?: { unread: boolean; onOpen: () => void }) {
  return renderToString(
    <PrimaryNavRail
      activeView="home"
      onNavigate={() => {}}
      pinned
      onTogglePinned={() => {}}
      whatsNew={whatsNew}
    />,
  );
}

describe("PrimaryNavRail", () => {
  it("always surfaces Capacity — the view reads cluster capacity across every node manager, not just Karpenter", () => {
    expect(renderRail()).toContain("容量");
  });

  it("shows What's new only when the host has notes, and marks it while unread", () => {
    expect(renderRail()).not.toContain("更新内容");
    const read = renderRail({ unread: false, onOpen: () => {} });
    expect(read).toContain("更新内容");
    expect(read).not.toContain("（未读）");
    expect(renderRail({ unread: true, onOpen: () => {} })).toContain("（未读）");
  });
});
