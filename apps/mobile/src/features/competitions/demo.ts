import { colors } from '@/design/tokens';
import { CompetitionSummary } from './types';

export const featuredCompetitions: CompetitionSummary[] = [
  { id: 'nairobi-sunday', number: '01', name: 'Nairobi Sunday Knockout', description: 'A fast 64-player single-elimination test for Nairobi\'s sharpest mobile players.', mode: '1v1', status: 'Check-in 18:30', players: '48 / 64', capacity: '75%', accent: colors.acid },
  { id: 'coast-rising', number: '02', name: 'Coast Rising Stars', description: 'A community competition for the next wave of Kenyan eFootball talent.', mode: '1v1', status: 'Registration open', players: '22 / 32', capacity: '69%', accent: colors.blue },
  { id: 'gamics-open-01', number: '03', name: 'Gamics Open #01', description: 'A national double-elimination competition with a second chance built in.', mode: 'Double elim.', status: 'Starts Saturday', players: '91 / 128', capacity: '71%', accent: colors.orange },
];
