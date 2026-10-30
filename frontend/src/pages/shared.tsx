import { useEffect, useState } from 'react';
import { listAll } from '../api';
import { useFetch } from '../ui';

export interface Term {
  id: number;
  code: string;
  name: string;
  registration_open: boolean;
}

export interface Sec {
  id: number;
  term_id: number;
  course_id: number;
  code: string;
  instructor_id: number | null;
  capacity: number;
  enrolled_count: number;
  waitlist_size: number;
  course_code: string;
  course_name: string;
  ects: number;
}

export interface Enr {
  id: number;
  section_id: number;
  status: 'enrolled' | 'waitlisted';
  course_code: string;
  course_name: string;
  ects: number;
  advisor_status: 'pending' | 'approved' | 'rejected';
  waitlist_position?: number;
}

/** Dönem listesini getirir ve varsayılan olarak kaydı açık dönemi (yoksa sonuncuyu) seçer. */
export function useTermSelect() {
  const state = useFetch(() => listAll<Term>('/terms'), []);
  const [termId, setTermId] = useState<number | null>(null);
  useEffect(() => {
    const t = state.data;
    if (t && t.length > 0 && termId === null) {
      setTermId((t.find((x) => x.registration_open) ?? t[t.length - 1]).id);
    }
  }, [state.data, termId]);
  return { state, terms: state.data ?? [], termId, setTermId };
}
