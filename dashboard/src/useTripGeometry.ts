import { useEffect, useRef, useState } from "react";
import {
  fetchTripGeometry,
  TripGeometryLoader,
  type GeometryMap,
} from "./tripGeometry";
import type { Snapshot } from "./types";

export function useTripGeometry(snapshot: Snapshot | null, visible: string[]) {
  const [geometry, setGeometry] = useState<GeometryMap>({});
  const [geometryError, setGeometryError] = useState("");
  const loader = useRef<TripGeometryLoader | null>(null);
  if (!loader.current)
    loader.current = new TripGeometryLoader(
      fetchTripGeometry,
      setGeometry,
      setGeometryError,
    );
  useEffect(() => {
    loader.current!.update(snapshot, visible);
  }, [snapshot, visible]);
  useEffect(() => () => loader.current!.dispose(), []);
  return { geometry, geometryError };
}
