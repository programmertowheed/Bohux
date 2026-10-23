import { keepLast, stripANSI } from "./logs";

test("ANSI colors and titles are removed", () => {
  expect(stripANSI("\u001b[31mred\u001b[0m and \u001b]0;title\u0007plain")).toBe("red and plain");
});

test("only the newest lines are kept", () => {
  expect(keepLast([1, 2, 3, 4], 2)).toEqual([3, 4]);
  expect(keepLast([1], 2)).toEqual([1]);
});
