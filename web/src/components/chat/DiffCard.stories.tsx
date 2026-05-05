// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { DiffCard } from "./DiffCard";

const SAMPLE = [
  "diff --git a/internal/cortex/lane.go b/internal/cortex/lane.go",
  "index 7c1a8b9..1f9e8d3 100644",
  "--- a/internal/cortex/lane.go",
  "+++ b/internal/cortex/lane.go",
  "@@ -42,7 +42,9 @@ func (l *Lane) Apply(ev Event) error {",
  "  if l.closed {",
  "    return errLaneClosed",
  "  }",
  "+ if ev.Seq <= l.lastSeq {",
  "+   return errOutOfOrder",
  "+ }",
  "  return l.transition(ev)",
  " }",
  "diff --git a/internal/cortex/lane_test.go b/internal/cortex/lane_test.go",
  "index 9b2e91c..abcdef1 100644",
  "--- a/internal/cortex/lane_test.go",
  "+++ b/internal/cortex/lane_test.go",
  "@@ -100,3 +100,12 @@ func TestApply_HappyPath(t *testing.T) {",
  "  // existing test body unchanged",
  " }",
  "+",
  "+func TestApply_RejectsOutOfOrder(t *testing.T) {",
  "+  l := newLane()",
  "+  if err := l.Apply(Event{Seq: 5}); err != nil {",
  "+    t.Fatal(err)",
  "+  }",
  "+  if err := l.Apply(Event{Seq: 3}); err == nil {",
  "+    t.Fatal(\"expected out-of-order rejection\")",
  "+  }",
  "+}",
].join("\n");

const meta: Meta<typeof DiffCard> = {
  title: "chat/DiffCard",
  component: DiffCard,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof DiffCard>;

export const TwoFiles: Story = {
  render: () => (
    <div className="w-[720px]">
      <DiffCard id="story-1" laneLabel="lane-a" patch={SAMPLE} />
    </div>
  ),
};

export const Split: Story = {
  render: () => (
    <div className="w-[720px]">
      <DiffCard id="story-2" laneLabel="lane-a" patch={SAMPLE} viewType="split" />
    </div>
  ),
};

export const Empty: Story = {
  render: () => (
    <div className="w-[720px]">
      <DiffCard id="story-3" laneLabel="lane-a" patch={null} />
    </div>
  ),
};
